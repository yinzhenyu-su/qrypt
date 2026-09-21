package readcache

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testAccess is the read run that admits chunks in tests that do not care about
// run identity. Using the same run on both sides keeps those chunks unproven,
// which is what a test that only exercises storage layering wants.
var testAccess = Access{Run: 1}

// checkInvariants cross-checks the store's byte accounting after a mutation.
// DebugSnapshot sums the chunk index independently of the readBytes counter, so
// comparing them catches a total that a lifecycle path let drift — the failure
// mode the derived-only policy avoids by construction.
func checkInvariants(t *testing.T, c *Store) {
	t.Helper()
	var summed int64
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for _, fc := range sh.chunks {
			fc.mu.RLock()
			for _, info := range fc.chunks {
				summed += info.size
			}
			fc.mu.RUnlock()
		}
		sh.mu.RUnlock()
	}
	if got := c.readBytes.Load(); got != summed {
		t.Fatalf("readBytes = %d, chunks sum to %d", got, summed)
	}
	snapshot := c.DebugSnapshot()
	if snapshot.Bytes != summed {
		t.Fatalf("snapshot bytes = %d, chunks sum to %d", snapshot.Bytes, summed)
	}
	if snapshot.Bytes != snapshot.UnprovenBytes+snapshot.ProvenBytes {
		t.Fatalf("unproven %d + proven %d != total %d",
			snapshot.UnprovenBytes, snapshot.ProvenBytes, snapshot.Bytes)
	}
	if snapshot.ProvenBytes != snapshot.ProbationaryBytes+snapshot.ProtectedBytes {
		t.Fatalf("probationary %d + protected %d != proven %d",
			snapshot.ProbationaryBytes, snapshot.ProtectedBytes, snapshot.ProvenBytes)
	}
	if snapshot.Bytes != snapshot.LargeFileBytes+snapshot.SmallFileBytes {
		t.Fatalf("large %d + small %d != total %d",
			snapshot.LargeFileBytes, snapshot.SmallFileBytes, snapshot.Bytes)
	}
	if snapshot.StreamBytes > snapshot.UnprovenBytes {
		t.Fatalf("stream bytes %d exceed unproven bytes %d",
			snapshot.StreamBytes, snapshot.UnprovenBytes)
	}
	if snapshot.Bytes > snapshot.MaxBytes {
		t.Fatalf("occupancy %d exceeds max_size %d", snapshot.Bytes, snapshot.MaxBytes)
	}
}

// A one-pass stream is bounded by the unproven sub-budget of the large-file
// class, so while the flow is over capacity the small-file class keeps its
// content and the stream gives up its own oldest chunks.
func TestEvictionKeepsSmallClassWhileLargeClassIsOverBudget(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 4*readChunkSize)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	smallKey := strings.Repeat("a", sha256.Size*2)
	largeKey := strings.Repeat("b", sha256.Size*2)

	if err := cache.PutChunk(smallKey, 1<<20, 0, chunk, testAccess); err != nil {
		t.Fatal(err)
	}
	for i := range int64(5) {
		if err := cache.PutChunk(largeKey, 20<<20, i, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := cache.GetChunk(smallKey, 0, testAccess); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("small-class chunk was evicted while the large class was over budget")
	}
	var largeChunks int
	for i := range int64(5) {
		if _, ok, err := cache.GetChunk(largeKey, i, testAccess); err != nil {
			t.Fatal(err)
		} else if ok {
			largeChunks++
		}
	}
	if largeChunks >= 5 {
		t.Fatalf("large-class chunks were not evicted, still have %d", largeChunks)
	}
	checkInvariants(t, cache)
}

// A file whose size was never learned still counts as large once its cached
// bytes reach the large-file threshold, so it is subject to the class budget.
func TestEvictionTreatsUnknownSizeCachedFileAsLarge(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 17*1024*1024)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	smallKey := strings.Repeat("a", sha256.Size*2)
	unknownKey := strings.Repeat("b", sha256.Size*2)

	if err := cache.PutChunk(smallKey, 1<<20, 0, chunk, testAccess); err != nil {
		t.Fatal(err)
	}
	unknownChunks := int64(17*1024*1024/readChunkSize + 1)
	for i := range unknownChunks {
		if err := cache.PutChunk(unknownKey, 0, i, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := cache.GetChunk(smallKey, 0, testAccess); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("small-class chunk was evicted before the unknown-size cached file")
	}
	var kept int64
	for i := range unknownChunks {
		if _, ok, err := cache.GetChunk(unknownKey, i, testAccess); err != nil {
			t.Fatal(err)
		} else if ok {
			kept++
		}
	}
	if kept >= unknownChunks {
		t.Fatalf("unknown-size cached file was not treated as large, still have %d chunks", kept)
	}
	checkInvariants(t, cache)
}

// The unproven sub-budget is what bounds a sequential pass: when a drain runs,
// unproven content is cut back to half of the large class's ceiling and the
// small class keeps its floor. A cache with free space is allowed to hold more
// than the budget — the budget decides who gives way, not what may be kept.
func TestEvictionCapsUnprovenSubBudget(t *testing.T) {
	t.Parallel()
	const maxSize = 8 << 20
	cache := newTestStore(t, t.TempDir(), maxSize)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	smallKey := strings.Repeat("a", sha256.Size*2)
	streamKey := strings.Repeat("d", sha256.Size*2)

	if err := cache.PutChunk(smallKey, 1<<20, 0, chunk, testAccess); err != nil {
		t.Fatal(err)
	}
	// Put until the first drain runs, then inspect its immediate result.
	var index int64
	for ; index < 200; index++ {
		if err := cache.PutChunk(streamKey, 64<<20, index, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
		if cache.DebugSnapshot().Evicted > 0 {
			break
		}
	}
	if index == 200 {
		t.Fatal("a 20MiB sequential pass never triggered a drain")
	}

	snapshot := cache.DebugSnapshot()
	// The large class may use what the small class is not holding, but never so
	// much that the small class could not still reach its floor.
	largeCeiling := int64(maxSize) - min(snapshot.SmallFileBytes, int64(maxSize)/readCacheSmallReserveDiv)
	if want := largeCeiling / readCacheStreamShare; snapshot.StreamBytes > want {
		t.Fatalf("stream bytes = %d, want at most %d (large ceiling %d): %+v",
			snapshot.StreamBytes, want, largeCeiling, snapshot)
	}
	if _, ok, err := cache.GetChunk(smallKey, 0, Access{Run: 99}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("small-class chunk did not survive the sequential pass")
	}
	checkInvariants(t, cache)
}

// A hit carrying the admitting run is the stream reading back its own read-ahead
// and must not promote; a hit from a later run means the content was reached
// again and must.
func TestEvictionPromotesOnlyOnALaterRun(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 8<<20)
	fid := strings.Repeat("c", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)

	if err := cache.PutChunk(fid, 20<<20, 0, chunk, Access{Run: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.GetChunk(fid, 0, Access{Run: 1}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("chunk missed")
	}
	if got := cache.DebugSnapshot(); got.UnprovenBytes != int64(readChunkSize) || got.Promotions != 0 {
		t.Fatalf("a same-run hit promoted the chunk: %+v", got)
	}

	if _, ok, err := cache.GetChunk(fid, 0, Access{Run: 2}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("chunk missed")
	}
	if got := cache.DebugSnapshot(); got.UnprovenBytes != 0 || got.ProtectedBytes != int64(readChunkSize) || got.Promotions != 1 {
		t.Fatalf("a later-run hit did not promote the chunk: %+v", got)
	}
	checkInvariants(t, cache)
}

// A caller that carries no run identity cannot be compared against the run that
// admitted a chunk, so the hit counts as reuse. Retention then degrades to
// "promote on first hit", which is the safe direction: the alternative — never
// promoting — would leave such a caller's content permanently evictable.
func TestEvictionTreatsAnUnknownRunAsReuse(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 8<<20)
	fid := strings.Repeat("e", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)

	if err := cache.PutChunk(fid, 20<<20, 0, chunk, Access{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := cache.GetChunk(fid, 0, Access{}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("chunk missed")
	}
	if got := cache.DebugSnapshot(); got.ProtectedBytes != int64(readChunkSize) || got.Promotions != 1 {
		t.Fatalf("an unknown run did not count as reuse: %+v", got)
	}
	checkInvariants(t, cache)
}

// The unproven bucket is drained by two phases when the stream sub-budget and
// the class ceiling both have to give: a plan must not select the same chunk
// twice, or the view's byte totals would be reduced twice for one chunk and the
// drain would stop short of the budget it was asked to reach.
func TestEvictionPlanSelectsEachChunkOnce(t *testing.T) {
	t.Parallel()
	const maxSize = 16 << 20
	cache := &Store{maxSize: maxSize}
	for i := range cache.shards {
		cache.shards[i].chunks = map[string]*fileChunks{}
	}
	at := time.Now()
	add := func(fid string, fileSize, index int64, state chunkState) {
		fc := cache.fileChunks(fid, fileSize)
		fc.mu.Lock()
		fc.chunks[index] = chunkInfo{
			file:     fid + ".batch",
			offset:   index * readChunkSize,
			size:     readChunkSize,
			accessAt: at.Add(time.Duration(index) * time.Millisecond),
			state:    state,
		}
		fc.mu.Unlock()
		cache.readBytes.Add(readChunkSize)
	}
	// 9MiB of unproven content (over the 8MiB sub-budget once the small class
	// holds nothing) plus 10MiB of proven content: the class is then over its
	// own ceiling too, so phase 2 drains the unproven bucket a second time.
	unproven := strings.Repeat("u", sha256.Size*2)
	proven := strings.Repeat("p", sha256.Size*2)
	for i := range int64(18) {
		add(unproven, 512<<20, i, stateUnproven)
	}
	for i := range int64(20) {
		add(proven, 512<<20, i, stateProtected)
	}

	plan := cache.planEviction(maxSize)
	if len(plan.victims) == 0 {
		t.Fatal("over-budget cache produced no victims")
	}
	seen := map[victim]bool{}
	for _, v := range plan.victims {
		key := victim{fid: v.fid, index: v.index}
		if seen[key] {
			t.Fatalf("chunk %s/%d selected twice", v.fid[:8], v.index)
		}
		seen[key] = true
	}
	// The plan must account for enough bytes to reach the low watermark.
	var planned int64
	for _, v := range plan.victims {
		planned += v.expected.size
	}
	lowWatermark := int64(maxSize) * readCacheEvictLowWatermarkNum / readCacheEvictLowWatermarkDen
	if cache.readBytes.Load()-planned > lowWatermark {
		t.Fatalf("plan frees %d of %d, leaving %d above the low watermark %d",
			planned, cache.readBytes.Load(), cache.readBytes.Load()-planned, lowWatermark)
	}
}

// A drain stops at the low watermark rather than at the cap, so the writes that
// follow it do not each trigger another pass.
func TestEvictionDrainsToTheLowWatermark(t *testing.T) {
	t.Parallel()
	const maxSize = 8 << 20
	cache := newTestStore(t, t.TempDir(), maxSize)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	// A small-class file has no sub-budget, so its growth reaches the capacity
	// edge and the low watermark is what stops the drain.
	fid := strings.Repeat("5", sha256.Size*2)
	var index int64
	for ; index < 200; index++ {
		if err := cache.PutChunk(fid, 1<<20, index, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
		if cache.DebugSnapshot().Evicted > 0 {
			break
		}
	}
	if index == 200 {
		t.Fatal("filling the cache never triggered a drain")
	}

	lowWatermark := int64(maxSize) * readCacheEvictLowWatermarkNum / readCacheEvictLowWatermarkDen
	snapshot := cache.DebugSnapshot()
	if snapshot.Bytes > lowWatermark {
		t.Fatalf("occupancy %d was not drained to the low watermark %d", snapshot.Bytes, lowWatermark)
	}
	checkInvariants(t, cache)

	// Everything the drain freed beyond the minimum, down to the cap, is
	// hysteresis: four more chunks fit without another pass.
	before := snapshot.Evicted
	for i := range int64(4) {
		if err := cache.PutChunk(fid, 1<<20, index+1+i, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
	}
	if after := cache.DebugSnapshot().Evicted; after != before {
		t.Fatalf("eviction re-ran %d times inside the hysteresis band", after-before)
	}
	checkInvariants(t, cache)
}

// Among equally old candidates the policy takes from the batch file closest to
// being unlinked: a batch file is released as a whole, so that is the only
// choice that returns disk space.
func TestEvictionPrefersTheBatchThatIsNearlyEmpty(t *testing.T) {
	t.Parallel()
	cache := &Store{maxSize: 2 * readChunkSize}
	for i := range cache.shards {
		cache.shards[i].chunks = map[string]*fileChunks{}
	}
	at := time.Unix(1000, 0)
	add := func(fid, file string, index int64) {
		fc := cache.fileChunks(fid, 1<<20)
		fc.mu.Lock()
		fc.chunks[index] = chunkInfo{
			file:     file,
			offset:   index * readChunkSize,
			size:     readChunkSize,
			accessAt: at,
		}
		fc.mu.Unlock()
		cache.readBytes.Add(readChunkSize)
	}
	fullKey := strings.Repeat("f", sha256.Size*2)
	sparseKey := strings.Repeat("s", sha256.Size*2)
	for i := range int64(cacheBatchBlocks) {
		add(fullKey, "full.batch", i)
	}
	add(sparseKey, "sparse.batch", 0)

	plan := cache.planEviction(cache.maxSize)
	if len(plan.victims) == 0 {
		t.Fatal("over-budget cache produced no victims")
	}
	if got := plan.victims[0].fid; got != sparseKey {
		t.Fatalf("first victim belongs to %q, want the sparse batch %q", got, sparseKey)
	}
	// The full batch is still referenced, so its file must stay on disk.
	if got := plan.victims[0].expected.file; got != "sparse.batch" {
		t.Fatalf("first victim file = %q, want sparse.batch", got)
	}
}

// The reclaim unit is the batch file: it is unlinked with its last chunk and not
// before, which is why the drain order prefers to complete a batch.
func TestEvictionReleasesBatchFileWithItsLastChunk(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 64<<20)
	fid := strings.Repeat("7", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	const chunks = 4
	for i := range int64(chunks) {
		if err := cache.PutChunk(fid, 64<<20, i, chunk, testAccess); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(cache.dir, "*.batch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("batch files = %v, want one", matches)
	}

	for i := range int64(chunks - 1) {
		if !cache.removeReadChunk(fid, i, chunkInfo{file: matches[0], offset: i * readChunkSize}) {
			t.Fatalf("chunk %d was not removed", i)
		}
		if _, err := os.Stat(matches[0]); err != nil {
			t.Fatalf("batch file removed while %d chunks still referenced it: %v", chunks-int(i)-1, err)
		}
	}
	if !cache.removeReadChunk(fid, chunks-1, chunkInfo{file: matches[0], offset: (chunks - 1) * readChunkSize}) {
		t.Fatal("last chunk was not removed")
	}
	if _, err := os.Stat(matches[0]); !os.IsNotExist(err) {
		t.Fatalf("batch file survived its last chunk, err=%v", err)
	}
	if got := cache.DebugSnapshot(); got.EvictedBytes != int64(chunks)*readChunkSize {
		t.Fatalf("evicted bytes = %d, want %d", got.EvictedBytes, int64(chunks)*readChunkSize)
	}
	checkInvariants(t, cache)
}

// Replacement state is in-memory only, so the persisted index keeps its shape
// and an existing cache directory survives the upgrade without a wipe.
func TestReadCacheIndexFormatIsUnchanged(t *testing.T) {
	t.Parallel()
	if readCacheIndexVersion != 1 {
		t.Fatalf("readCacheIndexVersion = %d; replacement state must not need a format bump", readCacheIndexVersion)
	}
	cache := newTestStore(t, t.TempDir(), 8<<20)
	fid := strings.Repeat("8", sha256.Size*2)
	if err := cache.PutChunk(fid, 1<<20, 0, []byte("cached"), testAccess); err != nil {
		t.Fatal(err)
	}
	if err := cache.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cache.dir, readCacheIndexName))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"admit_run", "segment", "proven", "\"state\""} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("persisted index carries replacement state %q: %s", banned, raw)
		}
	}
}

// Content loaded back from disk starts unproven, and the first run that comes
// back for it promotes it. That is what lets the index stay format-stable: the
// replacement state is rebuilt by use instead of migrated.
func TestReadCacheRestartStartsContentUnproven(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	chunk := []byte("cached")

	first := newTestStore(t, dir, 10<<20)
	if err := first.PutChunk(key, int64(len(chunk)), 0, chunk, Access{Run: 7}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := newTestStore(t, dir, 10<<20)
	if got := second.DebugSnapshot(); got.UnprovenBytes != int64(len(chunk)) {
		t.Fatalf("loaded content is not unproven: %+v", got)
	}
	if _, ok, err := second.GetChunk(key, 0, Access{Run: 9}); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("loaded chunk missed")
	}
	if got := second.DebugSnapshot(); got.UnprovenBytes != 0 || got.Promotions != 1 {
		t.Fatalf("a later run did not promote loaded content: %+v", got)
	}
	checkInvariants(t, second)
}

// Concurrent writers keep the derived totals exact; run under -race this also
// covers the policy's locking against the shard structure.
func TestEvictionConcurrentWritersKeepInvariants(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 64*readChunkSize)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() {
			fid := fmt.Sprintf("fid-%d", worker)
			run := uint64(worker + 1)
			for i := range int64(64) {
				if err := cache.PutChunk(fid, 64<<20, i, chunk, Access{Run: run}); err != nil {
					t.Error(err)
					return
				}
				if _, _, err := cache.GetChunkRange(fid, i, 0, readChunkSize, Access{Run: run}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()

	checkInvariants(t, cache)
	if got := cache.DebugSnapshot(); got.Evicted == 0 {
		t.Fatal("no eviction ran; the test did not exercise the policy")
	}
}
