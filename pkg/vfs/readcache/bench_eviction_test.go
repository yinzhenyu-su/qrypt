package readcache

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

// This file replays synthetic access traces through the real policy so the
// cache can be judged on hit bytes rather than on the shape of its bookkeeping.
//
// The simulator drives production code for everything that decides retention —
// touchReadChunk for a hit, planEviction for the drain, applyEviction for the
// removal — over an index built by hand instead of by writing 512KiB files. So
// it measures policy quality; only the unlink of a batch file is skipped, since
// no file exists. Admission takes its recency from time.Now() exactly as
// putChunk does, so the LRU order the policy sees is the order the trace
// touched chunks in.

type evictionSim struct {
	store   *Store
	maxSize int64
	hits    map[string]int64
	misses  map[string]int64
}

func newEvictionSim(maxSize int64) *evictionSim {
	store := &Store{maxSize: maxSize, activity: map[string]time.Time{}}
	for i := range store.shards {
		store.shards[i].chunks = map[string]*fileChunks{}
	}
	return &evictionSim{
		store:   store,
		maxSize: maxSize,
		hits:    map[string]int64{},
		misses:  map[string]int64{},
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
			s.store.touchActivity(fid)
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
	}
	fc.mu.Unlock()
	s.store.readBytes.Add(readChunkSize)
	s.store.touchActivity(fid)
	s.evict()
}

func (s *evictionSim) evict() int {
	if s.store.readBytes.Load() <= s.maxSize {
		return 0
	}
	return s.store.applyEviction(s.store.planEviction(s.maxSize))
}

// Trace shapes. The cache is deliberately smaller than the working set so every
// access pattern below runs under capacity pressure.
const (
	traceMaxSize   = 16 << 20
	hotLargeChunks = 8 // 4MiB, re-read: the content worth keeping
	smallFileCount = 4 // 4 x 1MiB, also re-read
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
func runMixedTrace() *evictionSim {
	sim := newEvictionSim(traceMaxSize)
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
// claim of the policy: after a long one-pass scan, content that was reached for
// more than once is still cached, whatever its file size.
//
// The scan itself is not supposed to be retained: a single pass over a file far
// larger than the cache is admitted probationary and drained first.
func TestEvictionKeepsReusableContentThroughASequentialScan(t *testing.T) {
	t.Parallel()
	sim := runMixedTrace()

	if got := sim.misses["hotLarge"]; got != 0 {
		t.Fatalf("reusable large file took %d bytes of misses after the scan (hits %d); "+
			"the scan displaced content that was read twice",
			got, sim.hits["hotLarge"])
	}
	if got := sim.misses["small"]; got != 0 {
		t.Fatalf("small content took %d bytes of misses after the scan (hits %d)",
			got, sim.hits["small"])
	}
	if sim.misses["scan"] != 0 {
		t.Fatalf("the scan was re-read in the measurement window: %v", sim.misses)
	}
	t.Logf("reusable set after the scan — %v hits / %v misses (scan misses %d)",
		sim.hits, sim.misses, sim.misses["scan"])
}

// benchPlanStore builds an over-capacity cache with a mix of probationary
// content (the tail an in-flight stream just admitted) and protected content
// (chunks a later run already proved reusable).
func benchPlanStore(b *testing.B, chunks int) *Store {
	b.Helper()
	sim := newEvictionSim(traceMaxSize)
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
	probationary := strings.Repeat("1", sha256.Size*2)
	protected := strings.Repeat("2", sha256.Size*2)
	for i := range int64(chunks) {
		if i%3 == 0 {
			add(protected, 512<<20, i, stateProtected)
			continue
		}
		add(probationary, 512<<20, i, stateProbationary)
	}
	return sim.store
}

// BenchmarkEvictionPlan measures the drain decision on an over-capacity cache
// with both segments populated.
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
