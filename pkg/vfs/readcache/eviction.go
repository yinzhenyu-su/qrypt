package readcache

import (
	"cmp"
	"os"
	"slices"
	"strings"
	"time"
)

// --- eviction.go ---
//
// The eviction policy is a decision over a compact view of the cache: one pass
// over the shards classifies every chunk into a budget bucket, and the drain
// order falls out of the budget tree. No byte total is maintained
// incrementally, so a lifecycle path that removes chunks behind the policy's
// back (invalidate, clear, a stale chunk dropped on read, a whole-file replace)
// cannot drift a counter out of sync with the contents.
//
// The budget tree has one level per failure mode it prevents:
//
//	maxSize
//	├── small class          floor maxSize/readCacheSmallReserveDiv
//	└── large class          ceiling maxSize - maxSize/readCacheSmallReserveDiv
//	      ├── unproven       <= ceiling/readCacheStreamShare
//	      └── proven         the rest
//	      within a class: protected <= occupancy - occupancy/readCacheProtectedShare
//
// A chunk is unproven until a read run other than the one that admitted it
// touches it (see Access). That is what keeps a one-pass sequential stream —
// which reads back its own read-ahead and would otherwise look hot — inside its
// sub-budget instead of evicting reusable content.

// diskCheckInterval is how often eviction re-checks free disk space. If the
// filesystem is filling up from other processes, the cap tightens so the cache
// does not cause a disk-full.
const diskCheckInterval = 10 * time.Second

// sameRunAs reports whether this access belongs to the read run that admitted a
// chunk. A zero Run means the caller has no run identity, so the access is
// treated as a new run and counts as reuse.
func (a Access) sameRunAs(admitRun uint64) bool {
	return a.Run != 0 && a.Run == admitRun
}

// evictIfNeeded returns the cache within its capacity after a write.
func (c *Store) evictIfNeeded() {
	maxSize := c.maxSize
	if maxSize <= 0 {
		return
	}
	if lastCheck := time.Unix(0, c.lastDiskCheck.Load()); time.Since(lastCheck) >= diskCheckInterval {
		c.lastDiskCheck.Store(time.Now().UnixNano())
		if adjusted, _ := limitByDiskSpace(maxSize, c.dir); adjusted < maxSize {
			maxSize = adjusted
		}
	}
	if c.readBytes.Load() <= maxSize {
		return
	}
	plan := c.planEviction(maxSize)
	removed := c.applyEviction(plan)
	if removed == 0 && len(plan.demote) == 0 {
		return
	}
	c.log.Infof("[CACHE] evicted %d chunks demoted=%d size=%d max_size=%d",
		removed, len(plan.demote), c.readBytes.Load(), maxSize)
	c.scheduleReadIndexSave()
}

// touchReadChunk records a hit on a chunk: it refreshes the recency the drain
// order uses, and promotes the chunk when the hit comes from a different read
// run than the one that admitted it — a hit from the admitting run is the
// stream reading back its own read-ahead, not evidence of reuse.
//
// The recency write is coalesced to cacheAccessWriteInterval so a hot chunk does
// not take the file lock on every read. Promotion is applied even when the
// recency write is not due, so a re-read is not lost to the coalescing window.
func (c *Store) touchReadChunk(fc *fileChunks, index int64, seen chunkInfo, access Access) {
	now := time.Now()
	refresh := seen.accessAt.Add(cacheAccessWriteInterval).Before(now)
	fc.mu.RLock()
	current, ok := fc.chunks[index]
	fc.mu.RUnlock()
	if !ok || current.file != seen.file || current.offset != seen.offset {
		return
	}
	reuse := !access.sameRunAs(current.admitRun)
	if !refresh && !reuse {
		return
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	current, ok = fc.chunks[index]
	if !ok || current.file != seen.file || current.offset != seen.offset {
		return
	}
	if refresh {
		current.accessAt = now
	}
	if reuse && current.state != stateProtected {
		current.state = stateProtected
		c.stats.promotions.Add(1)
	}
	fc.chunks[index] = current
}

// chunkState is the replacement state of one cached chunk. It is in-memory only
// and deliberately absent from the persisted index.
type chunkState uint8

const (
	// stateUnproven means the admitting read run has not come back for this
	// chunk. This is what a sequential stream produces when it consumes its own
	// read-ahead, so it is not treated as reuse.
	stateUnproven chunkState = iota
	// stateProbationary means another run proved the chunk is reused; it is not
	// protected yet.
	stateProbationary
	// stateProtected is preferred over probationary content and is only evicted
	// when probationary content cannot satisfy the budget.
	stateProtected
)

// bucket is one drainable pool with its own byte budget.
type bucket uint8

const (
	bucketSmallProbationary bucket = iota
	bucketSmallProtected
	bucketLargeUnproven
	bucketLargeProbationary
	bucketLargeProtected
	numBuckets
)

// drainOrder is the sequence a capacity drain walks: inside the large class the
// unproven sub-budget goes first, then probationary, then protected; the small
// class gives up bytes only after the large class has nothing left to give.
var drainOrder = [...]bucket{
	bucketLargeUnproven,
	bucketLargeProbationary,
	bucketLargeProtected,
	bucketSmallProbationary,
	bucketSmallProtected,
}

func bucketFor(class sizeClass, state chunkState) bucket {
	if class == classSmall {
		if state == stateProtected {
			return bucketSmallProtected
		}
		return bucketSmallProbationary
	}
	switch state {
	case stateUnproven:
		return bucketLargeUnproven
	case stateProtected:
		return bucketLargeProtected
	default:
		return bucketLargeProbationary
	}
}

// probationaryBucket is the bucket a chunk of this class falls back to when it
// is demoted out of the protected segment.
func probationaryBucket(class sizeClass) bucket {
	if class == classSmall {
		return bucketSmallProbationary
	}
	return bucketLargeProbationary
}

// readCacheFileLarge reports whether a file belongs to the large-file class: a
// declared size at or above the threshold, or an unknown size whose cached
// bytes already reach it. The unknown-size arm covers chunks admitted before
// the file's size was known.
func readCacheFileLarge(fileSize, cachedBytes int64) bool {
	if fileSize >= ReadCacheLargeFileBytes {
		return true
	}
	return fileSize == 0 && cachedBytes >= ReadCacheLargeFileBytes
}

// victim names one chunk together with the chunkInfo the decision was based on.
// Removal re-checks it under the file lock, so a chunk that changed or moved
// after planning is skipped rather than deleted blindly.
type victim struct {
	fid      string
	index    int64
	expected chunkInfo
}

// evictionPlan is the policy's output: segment changes to apply and chunks to
// remove. Planning has no side effects on the store, so it can be unit-tested
// against a hand-built cache; the Store applies the plan.
type evictionPlan struct {
	demote  []victim
	victims []victim
	// selected marks the view chunks already chosen as victims, indexed like
	// cacheView.chunks. Two phases can drain the same bucket — the unproven
	// sub-budget and the class ceiling both cut unproven content back — and a
	// bucket's candidate list is not consumed by draining it, so without this a
	// chunk would be selected twice and the view would subtract its bytes twice.
	selected []bool
}

// viewChunk is one chunk as the policy sees it during planning.
type viewChunk struct {
	fid    string
	index  int64
	info   chunkInfo
	class  sizeClass
	bucket bucket
}

// cacheView is a point-in-time, compact picture of the cache contents. Its byte
// totals are derived during construction and then updated in place while the
// plan drains buckets, so every later decision sees the effect of the earlier
// ones.
type cacheView struct {
	chunks      []viewChunk
	byBucket    [numBuckets][]int
	total       int64
	classBytes  [numSizeClasses]int64
	bucketBytes [numBuckets]int64
	// batchRefs counts the live chunks in each batch file. It is a build-time
	// snapshot and never mutated afterwards: the victim order compares it, and
	// a comparator that changed while sorting would not be a total order.
	batchRefs map[string]int32
}

func (c *Store) buildView() *cacheView {
	view := &cacheView{batchRefs: map[string]int32{}}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for fid, fc := range sh.chunks {
			fc.mu.RLock()
			var cachedBytes int64
			for _, info := range fc.chunks {
				cachedBytes += info.size
			}
			class := classOf(fc.fileSize, cachedBytes)
			for index, info := range fc.chunks {
				b := bucketFor(class, info.state)
				view.chunks = append(view.chunks, viewChunk{
					fid:    fid,
					index:  index,
					info:   info,
					class:  class,
					bucket: b,
				})
				view.byBucket[b] = append(view.byBucket[b], len(view.chunks)-1)
				view.total += info.size
				view.classBytes[class] += info.size
				view.bucketBytes[b] += info.size
				view.batchRefs[info.file]++
			}
			fc.mu.RUnlock()
		}
		sh.mu.RUnlock()
	}
	return view
}

// evictionBudgets is maxSize expressed as the budget tree.
type evictionBudgets struct {
	lowWatermark  int64
	largeCeiling  int64
	streamCeiling int64
}

func deriveBudgets(maxSize int64, view *cacheView) evictionBudgets {
	floor := maxSize / readCacheSmallReserveDiv
	// The large class may use everything the small class is not currently
	// holding, but never so much that the small class can no longer reach its
	// floor. While the small class is below its floor this loosens the ceiling,
	// so a cache holding only large files does not waste capacity.
	largeCeiling := maxSize - min(view.classBytes[classSmall], floor)
	if largeCeiling < 0 {
		largeCeiling = 0
	}
	return evictionBudgets{
		lowWatermark:  maxSize * readCacheEvictLowWatermarkNum / readCacheEvictLowWatermarkDen,
		largeCeiling:  largeCeiling,
		streamCeiling: largeCeiling / readCacheStreamShare,
	}
}

// planEviction decides what must be removed so the cache returns within
// maxSize. The caller has already established that the cache is over budget.
func (c *Store) planEviction(maxSize int64) evictionPlan {
	view := c.buildView()
	if view.total <= maxSize {
		return evictionPlan{}
	}
	budgets := deriveBudgets(maxSize, view)
	plan := evictionPlan{}

	// 1. Unproven content inside the large class is capped first. This is the
	//    bound that stops one sequential pass from filling the class, and it
	//    does not depend on the cache being over maxSize to matter: it is a
	//    class budget, not a global one.
	if excess := view.bucketBytes[bucketLargeUnproven] - budgets.streamCeiling; excess > 0 {
		plan.drain(view, bucketLargeUnproven, excess)
	}
	// 2. Cap the large class as a whole so the small class can still grow to
	//    its floor.
	if excess := view.classBytes[classLarge] - budgets.largeCeiling; excess > 0 {
		plan.drain(view, bucketLargeUnproven, excess)
		plan.drain(view, bucketLargeProbationary, excess)
		plan.drain(view, bucketLargeProtected, excess)
	}
	// 3. Capacity: drain to the low watermark so the writes that follow do not
	//    each trigger another pass.
	if want := view.total - budgets.lowWatermark; want > 0 {
		plan.demoteOverflow(view)
		for _, b := range drainOrder {
			if want <= 0 {
				break
			}
			want -= plan.drain(view, b, want)
		}
	}
	return plan
}

// demoteOverflow moves the oldest protected chunks back to probationary when the
// protected segment exceeds its share of a class's occupancy. Demotion is not
// eviction: it decides which segment pays for the next drain, and it keeps
// probationary holding roughly a fifth of each class so a burst of promotions
// does not make every newly admitted chunk the next victim.
func (p *evictionPlan) demoteOverflow(view *cacheView) {
	for _, class := range []sizeClass{classSmall, classLarge} {
		from := protectedBucket(class)
		protected := view.bucketBytes[from]
		limit := view.classBytes[class] - view.classBytes[class]/readCacheProtectedShare
		if protected <= limit {
			continue
		}
		candidates := slices.Clone(view.byBucket[from])
		view.sortCandidates(candidates)
		to := probationaryBucket(class)
		for _, i := range candidates {
			if protected <= limit {
				break
			}
			if p.selected != nil && p.selected[i] {
				// Already chosen as a victim by an earlier phase; demoting a
				// chunk that is about to be removed is wasted work.
				continue
			}
			chunk := view.chunks[i]
			p.demote = append(p.demote, victim{fid: chunk.fid, index: chunk.index, expected: chunk.info})
			protected -= chunk.info.size
			view.bucketBytes[from] -= chunk.info.size
			view.bucketBytes[to] += chunk.info.size
			view.chunks[i].bucket = to
			view.byBucket[to] = append(view.byBucket[to], i)
		}
	}
}

// protectedBucket is the drainable pool holding a class's protected segment.
func protectedBucket(class sizeClass) bucket {
	if class == classSmall {
		return bucketSmallProtected
	}
	return bucketLargeProtected
}

// drain selects victims from one bucket until want bytes are released from it or
// the bucket is empty, and returns what it released. The view's byte totals are
// updated so the caller's next decision sees the result.
func (p *evictionPlan) drain(view *cacheView, b bucket, want int64) int64 {
	if want <= 0 || view.bucketBytes[b] <= 0 {
		return 0
	}
	if p.selected == nil {
		p.selected = make([]bool, len(view.chunks))
	}
	candidates := view.byBucket[b]
	view.sortCandidates(candidates)
	var freed int64
	for _, i := range candidates {
		if freed >= want {
			break
		}
		if p.selected[i] {
			continue
		}
		chunk := view.chunks[i]
		p.selected[i] = true
		p.victims = append(p.victims, victim{fid: chunk.fid, index: chunk.index, expected: chunk.info})
		freed += chunk.info.size
		view.total -= chunk.info.size
		view.classBytes[chunk.class] -= chunk.info.size
		view.bucketBytes[b] -= chunk.info.size
	}
	return freed
}

// sortCandidates orders candidates oldest first. Chunks of the same age are
// ordered by how few live chunks their batch file still holds, because removing
// one brings that physical unit closer to being unlinked — the only way
// eviction actually returns disk space, since a batch file is released as a
// whole. The remaining keys make the order total, so planning is deterministic
// and testable.
func (v *cacheView) sortCandidates(candidates []int) {
	slices.SortFunc(candidates, func(i, j int) int {
		a, b := v.chunks[i], v.chunks[j]
		if !a.info.accessAt.Equal(b.info.accessAt) {
			if a.info.accessAt.Before(b.info.accessAt) {
				return -1
			}
			return 1
		}
		if a.info.file != b.info.file {
			if refs := cmp.Compare(v.batchRefs[a.info.file], v.batchRefs[b.info.file]); refs != 0 {
				return refs
			}
		}
		if fid := strings.Compare(a.fid, b.fid); fid != 0 {
			return fid
		}
		return cmp.Compare(a.index, b.index)
	})
}

// applyEviction carries out a plan: segment changes first, then removals. Both
// re-check the chunk they were planned against, so a concurrent read that
// promoted or re-wrote a chunk is not undone by stale bookkeeping.
func (c *Store) applyEviction(plan evictionPlan) int {
	for _, d := range plan.demote {
		c.demoteReadChunk(d)
	}
	var removed int
	for _, v := range plan.victims {
		if c.removeReadChunk(v.fid, v.index, v.expected) {
			removed++
		}
	}
	return removed
}

// demoteReadChunk moves one chunk out of the protected segment. It only fires
// while the chunk is still the one the plan saw, so a chunk that was promoted
// again in the meantime keeps its promotion.
func (c *Store) demoteReadChunk(v victim) bool {
	sh := c.shardFor(v.fid)
	sh.mu.RLock()
	fc := sh.chunks[v.fid]
	sh.mu.RUnlock()
	if fc == nil {
		return false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	current, ok := fc.chunks[v.index]
	if !ok || current.file != v.expected.file || current.offset != v.expected.offset {
		return false
	}
	if current.state != stateProtected || !current.accessAt.Equal(v.expected.accessAt) {
		return false
	}
	current.state = stateProbationary
	fc.chunks[v.index] = current
	return true
}

// removeReadChunk drops one chunk, deleting its batch file when no other chunk
// still references it.
func (c *Store) removeReadChunk(fid string, index int64, expected chunkInfo) bool {
	sh := c.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
	if fc == nil {
		return false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	current, ok := fc.chunks[index]
	if !ok || current.file != expected.file || current.offset != expected.offset {
		return false
	}
	stillReferenced := false
	for idx, ch := range fc.chunks {
		if ch.file == current.file && idx != index {
			stillReferenced = true
			break
		}
	}
	if !stillReferenced {
		_ = os.Remove(current.file)
	}
	delete(fc.chunks, index)
	c.readBytes.Add(-current.size)
	c.stats.evicted.Add(1)
	c.stats.evictedBytes.Add(current.size)
	return true
}
