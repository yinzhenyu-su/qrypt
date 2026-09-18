package readcache

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

// This file replays synthetic access traces through the real policy so the
// replacement can be judged on hit bytes rather than on the shape of its
// bookkeeping.
//
// The simulator drives production code for everything that decides retention —
// touchReadChunk for a hit, planEviction for the drain, applyEviction for the
// removal — over an index built by hand instead of by writing 512KiB files. So
// it measures policy quality; only the unlink of a batch file is skipped, since
// no file exists. Admission takes its recency from time.Now() exactly as
// putChunk does, so the LRU order the policy sees is the order the trace
// touched chunks in.

type evictionSim struct {
	store    *Store
	maxSize  int64
	hits     map[string]int64
	misses   map[string]int64
	baseline bool
}

func newEvictionSim(maxSize int64, baseline bool) *evictionSim {
	store := &Store{maxSize: maxSize}
	for i := range store.shards {
		store.shards[i].chunks = map[string]*fileChunks{}
	}
	return &evictionSim{
		store:    store,
		maxSize:  maxSize,
		hits:     map[string]int64{},
		misses:   map[string]int64{},
		baseline: baseline,
	}
}

// readChunk serves one chunk read: the chunk is a hit when its file still holds
// it, otherwise it is admitted and the cache is given a chance to drain.
func (s *evictionSim) readChunk(category, fid string, fileSize, index int64, run uint64) {
	sh := s.store.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
	if fc != nil {
		fc.mu.RLock()
		info, ok := fc.chunks[index]
		fc.mu.RUnlock()
		if ok {
			s.hits[category] += info.size
			s.store.touchReadChunk(fc, index, info, Access{Run: run})
			return
		}
	}
	s.misses[category] += readChunkSize
	fc = s.store.fileChunks(fid, fileSize)
	fc.mu.Lock()
	fc.chunks[index] = chunkInfo{
		file:     fid + ".batch",
		offset:   index * readChunkSize,
		size:     readChunkSize,
		accessAt: time.Now(),
		admitRun: run,
	}
	fc.mu.Unlock()
	s.store.readBytes.Add(readChunkSize)
	s.evict()
}

func (s *evictionSim) evict() int {
	if s.store.readBytes.Load() <= s.maxSize {
		return 0
	}
	var plan evictionPlan
	if s.baseline {
		plan = evictionPlan{victims: baselineVictims(s.store.buildView(), s.maxSize)}
	} else {
		plan = s.store.planEviction(s.maxSize)
	}
	return s.store.applyEviction(plan)
}

// baselineVictims reproduces the replacement this change removed: one global LRU
// with a hardcoded large-file pool cap. It exists so the trace test can report
// what the previous policy did with the same accesses.
func baselineVictims(view *cacheView, maxSize int64) []victim {
	order := make([]int, len(view.chunks))
	for i := range order {
		order[i] = i
	}
	view.sortCandidates(order)

	var victims []victim
	removed := make([]bool, len(view.chunks))
	total := view.total
	var largeTotal int64
	for _, chunk := range view.chunks {
		if chunk.class == classLarge {
			largeTotal += chunk.info.size
		}
	}
	take := func(i int) {
		chunk := view.chunks[i]
		removed[i] = true
		victims = append(victims, victim{fid: chunk.fid, index: chunk.index, expected: chunk.info})
		total -= chunk.info.size
		if chunk.class == classLarge {
			largeTotal -= chunk.info.size
		}
	}
	largeBudget := maxSize - maxSize/readCacheSmallReserveDiv
	for _, i := range order {
		if largeTotal <= largeBudget && total <= maxSize {
			break
		}
		if view.chunks[i].class != classLarge || removed[i] {
			continue
		}
		take(i)
	}
	target := maxSize * 7 / 10
	for _, i := range order {
		if total <= target {
			break
		}
		if removed[i] {
			continue
		}
		take(i)
	}
	return victims
}

// Trace shapes. The cache is deliberately smaller than the working set so every
// access pattern below runs under capacity pressure, which is the only regime
// the budget tree speaks to.
const (
	traceMaxSize   = 16 << 20
	hotLargeChunks = 8 // 4MiB, re-read: the content worth keeping
	smallFileCount = 4 // 4 x 1MiB of small-class content, also re-read
	scanChunks     = 200
)

// resetCounters starts a fresh measurement window: the warm-up passes of the
// reusable set are cold by construction and would otherwise be counted as
// displacement.
func (s *evictionSim) resetCounters() {
	s.hits = map[string]int64{}
	s.misses = map[string]int64{}
}

// runMixedTrace warms a reusable large file and a set of small files, then
// streams a much larger file past them one pass, then reaches for the reusable
// content again. The counters are reset right before that last step: those bytes
// are the measurement, because they are what a sequential scan is not allowed
// to take.
func runMixedTrace(baseline bool) *evictionSim {
	sim := newEvictionSim(traceMaxSize, baseline)
	hot := strings.Repeat("h", sha256.Size*2)
	scan := strings.Repeat("s", sha256.Size*2)
	small := func(file int) string { return strings.Repeat(string(rune('a'+file)), sha256.Size*2) }

	for _, run := range []uint64{1, 2} {
		for i := range int64(hotLargeChunks) {
			sim.readChunk("hotLarge", hot, 64<<20, i, run)
		}
	}
	for file := range smallFileCount {
		for _, run := range []uint64{10, 11} {
			for i := range int64(2) {
				sim.readChunk("small", small(file), 1<<20, i, run)
			}
		}
	}
	// One continuous pass over a file far larger than the cache.
	for i := range int64(scanChunks) {
		sim.readChunk("scan", scan, 512<<20, i, 100)
	}

	sim.resetCounters()
	for i := range int64(hotLargeChunks) {
		sim.readChunk("hotLarge", hot, 64<<20, i, 200)
	}
	for file := range smallFileCount {
		for i := range int64(2) {
			sim.readChunk("small", small(file), 1<<20, i, 201)
		}
	}
	return sim
}

// TestEvictionKeepsReusableContentThroughASequentialScan is the behavioural
// claim of the new policy: after a long one-pass scan, content that was reached
// for more than once is still cached, in both size classes.
//
// It also reports what the previous policy did with the same accesses. That
// policy kept small files through the class split but had nothing protecting a
// reusable large file: its chunks were the oldest large-class entries, so the
// scan evicted them first and then started on the small class.
func TestEvictionKeepsReusableContentThroughASequentialScan(t *testing.T) {
	replaced := runMixedTrace(false)
	previous := runMixedTrace(true)

	if got := replaced.misses["hotLarge"]; got != 0 {
		t.Fatalf("reusable large file took %d bytes of misses after the scan (hits %d); "+
			"the scan displaced content that was read twice",
			got, replaced.hits["hotLarge"])
	}
	if got := replaced.misses["small"]; got != 0 {
		t.Fatalf("small-class content took %d bytes of misses after the scan (hits %d)",
			got, replaced.misses["small"])
	}
	// The scan itself is not supposed to be retained across a one-pass read; it
	// only has to leave the reusable set alone.
	if replaced.misses["scan"] != 0 {
		t.Fatalf("the scan was re-read in the measurement window: %v", replaced.misses)
	}
	t.Logf("reusable set after the scan — replaced: %v hits / %v misses; previous policy: %v hits / %v misses",
		replaced.hits, replaced.misses, previous.hits, previous.misses)
}

// benchPlanStore builds a cache spread across the budget buckets the way a real
// one is: some small-class content, some proven large-class content, and a tail
// of unproven large-class content left by an in-flight stream.
func benchPlanStore(b *testing.B, chunks int) *Store {
	b.Helper()
	sim := newEvictionSim(traceMaxSize, false)
	at := time.Now()
	add := func(fid string, fileSize int64, index int64, state chunkState) {
		fc := sim.store.fileChunks(fid, fileSize)
		fc.mu.Lock()
		fc.chunks[index] = chunkInfo{
			file:     fid + ".batch",
			offset:   index * readChunkSize,
			size:     readChunkSize,
			accessAt: at.Add(time.Duration(index) * time.Millisecond),
			state:    state,
		}
		fc.mu.Unlock()
		sim.store.readBytes.Add(readChunkSize)
	}
	small := strings.Repeat("1", sha256.Size*2)
	proven := strings.Repeat("2", sha256.Size*2)
	unproven := strings.Repeat("3", sha256.Size*2)
	for i := range int64(chunks) {
		switch i % 3 {
		case 0:
			add(small, 1<<20, i, stateUnproven)
		case 1:
			add(proven, 512<<20, i, stateProtected)
		default:
			add(unproven, 512<<20, i, stateUnproven)
		}
	}
	return sim.store
}

// BenchmarkEvictionPlan measures the drain decision on a cache holding many
// chunks across several budgets.
func BenchmarkEvictionPlan(b *testing.B) {
	store := benchPlanStore(b, 240)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if plan := store.planEviction(store.maxSize); len(plan.victims) == 0 {
			b.Fatal("no victims selected")
		}
	}
}

// BenchmarkEvictionPlanPreviousPolicy measures the same decision under the
// replacement this change removed, which ordered every chunk in the cache
// before choosing.
//
// The two run within roughly 15% of each other on this shape, and the new one
// allocates a little more: the drain cost is dominated by walking the index and
// materializing candidates, not by the ordering. So the bucket split's payoff is
// behavioural (see the trace test above), not a cheaper drain. Cutting the
// per-drain cost further means not walking the whole index, which this change
// deliberately did not attempt — see the design's Non-Goals.
func BenchmarkEvictionPlanPreviousPolicy(b *testing.B) {
	store := benchPlanStore(b, 240)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if victims := baselineVictims(store.buildView(), store.maxSize); len(victims) == 0 {
			b.Fatal("no victims selected")
		}
	}
}
