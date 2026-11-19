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
// over the shards sorts every chunk into one of two segments, and the drain
// order falls out of the store's single byte budget.
//
//	maxSize                        one continuous budget
//	├── probationary               reached by one read pass; drained first
//	└── protected                  reached by a later run; drained last
//	    protected <= total - total/readCacheProtectedShare
//
// A chunk starts probationary and is protected only when a read run other than
// the one that admitted it touches it (see Access): the admitting run reading
// back its own read-ahead is not evidence of reuse.
//
// The per-file pin is what keeps one stalled stream from evicting its own
// read-ahead. Every chunk of a file that saw a read or a write inside
// readCachePinWindow is skipped by the first drain pass; pinned chunks are only
// evicted when the unpinned contents cannot bring the cache back within its
// budget, so the pin never suspends the capacity bound.

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

// evictIfNeeded returns the cache within maxSize after a write.
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
// run than the one that admitted it.
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
	// stateProbationary is what a chunk a single read pass produced looks like:
	// nothing has come back for it yet.
	stateProbationary chunkState = iota
	// stateProtected is a chunk another read run came back for; it is drained
	// only after probationary content.
	stateProtected
)

// segment is one drainable pool of the single budget.
type segment uint8

const (
	segmentProbationary segment = iota
	segmentProtected
	numSegments
)

// drainOrder is the sequence a capacity drain walks: probationary content first
// so the newly admitted chunks pay for the overflow, then protected reuse.
var drainOrder = [...]segment{segmentProbationary, segmentProtected}

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
	// cacheView.chunks. A bucket's candidate list is not consumed by draining
	// it, so without this a chunk would be selected twice and the view would
	// subtract its bytes twice.
	selected []bool
}

// viewChunk is one chunk as the policy sees it during planning.
type viewChunk struct {
	fid    string
	index  int64
	info   chunkInfo
	seg    segment
	pinned bool
}

// cacheView is a point-in-time, compact picture of the cache contents. Its byte
// totals are derived during construction and then updated in place while the
// plan drains segments, so every later decision sees the effect of the earlier
// ones.
type cacheView struct {
	chunks       []viewChunk
	bySegment    [numSegments][]int
	total        int64
	segmentBytes [numSegments]int64
	pinnedBytes  int64
	// batchRefs counts the live chunks in each batch file. It is a build-time
	// snapshot and never mutated afterwards: the victim order compares it, and
	// a comparator that changed while sorting would not be a total order.
	batchRefs map[string]int32
}

func (c *Store) buildView() *cacheView {
	view := &cacheView{batchRefs: map[string]int32{}}
	now := time.Now()
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for fid, fc := range sh.chunks {
			pinned := c.filePinned(fid, now)
			fc.mu.RLock()
			for index, info := range fc.chunks {
				seg := segmentProbationary
				if info.state == stateProtected {
					seg = segmentProtected
				}
				view.chunks = append(view.chunks, viewChunk{
					fid:    fid,
					index:  index,
					info:   info,
					seg:    seg,
					pinned: pinned,
				})
				view.bySegment[seg] = append(view.bySegment[seg], len(view.chunks)-1)
				view.total += info.size
				view.segmentBytes[seg] += info.size
				if pinned {
					view.pinnedBytes += info.size
				}
				view.batchRefs[info.file]++
			}
			fc.mu.RUnlock()
		}
		sh.mu.RUnlock()
	}
	return view
}

// filePinned reports whether a file saw a read or a write inside the pin
// window, which keeps its chunks out of the first drain pass.
func (c *Store) filePinned(fid string, now time.Time) bool {
	c.activityMu.Lock()
	at, ok := c.activity[fid]
	c.activityMu.Unlock()
	return ok && now.Sub(at) <= readCachePinWindow
}

// touchActivity pins the chunks of a file that is being read or written now.
func (c *Store) touchActivity(fid string) {
	c.activityMu.Lock()
	if c.activity == nil {
		c.activity = map[string]time.Time{}
	}
	c.activity[fid] = time.Now()
	c.activityMu.Unlock()
}

// forgetActivity drops the pin record of a file that is no longer cached.
func (c *Store) forgetActivity(fid string) {
	c.activityMu.Lock()
	delete(c.activity, fid)
	c.activityMu.Unlock()
}

// evictionBudgets is maxSize expressed as the budget tree.
type evictionBudgets struct {
	lowWatermark   int64
	protectedLimit int64
}

func deriveBudgets(maxSize int64, view *cacheView) evictionBudgets {
	return evictionBudgets{
		lowWatermark: maxSize * readCacheEvictLowWatermarkNum / readCacheEvictLowWatermarkDen,
		// The protected segment may hold at most four fifths of the cache, so
		// the probationary segment the next drain takes from never empties.
		protectedLimit: view.total - view.total/readCacheProtectedShare,
	}
}

// planEviction decides what must be removed so the cache returns within maxSize.
func (c *Store) planEviction(maxSize int64) evictionPlan {
	view := c.buildView()
	plan := evictionPlan{}
	if view.total <= maxSize {
		return plan
	}
	budgets := deriveBudgets(maxSize, view)
	// Capacity: drain to the low watermark so the writes that follow do not
	// each trigger another pass.
	want := view.total - budgets.lowWatermark
	plan.demoteOverflow(view, budgets)
	// Unpinned content pays first, and a pinned file is only touched when the
	// unpinned contents cannot satisfy the drain.
	for _, seg := range drainOrder {
		if want <= 0 {
			break
		}
		want -= plan.drain(view, seg, want, true)
	}
	for _, seg := range drainOrder {
		if want <= 0 {
			break
		}
		want -= plan.drain(view, seg, want, false)
	}
	return plan
}

// demoteOverflow moves the oldest protected chunks back to probationary when the
// protected segment exceeds its share. Demotion is not eviction: it decides
// which segment pays for the next drain, and it keeps probationary holding
// roughly a fifth of the cache so a burst of promotions does not make every
// newly admitted chunk the next victim.
func (p *evictionPlan) demoteOverflow(view *cacheView, budgets evictionBudgets) {
	protected := view.segmentBytes[segmentProtected]
	limit := budgets.protectedLimit
	if protected <= limit {
		return
	}
	candidates := slices.Clone(view.bySegment[segmentProtected])
	view.sortCandidates(candidates)
	for _, i := range candidates {
		if protected <= limit {
			break
		}
		if p.selected != nil && p.selected[i] {
			// Already chosen as a victim by an earlier phase; demoting a chunk
			// that is about to be removed is wasted work.
			continue
		}
		chunk := view.chunks[i]
		p.demote = append(p.demote, victim{fid: chunk.fid, index: chunk.index, expected: chunk.info})
		protected -= chunk.info.size
		view.segmentBytes[segmentProtected] -= chunk.info.size
		view.segmentBytes[segmentProbationary] += chunk.info.size
		view.chunks[i].seg = segmentProbationary
		view.bySegment[segmentProbationary] = append(view.bySegment[segmentProbationary], i)
	}
}

// drain selects victims from one segment until want bytes are released from it
// or the segment is empty, and returns what it released. The view's byte totals
// are updated so the caller's next decision sees the result.
func (p *evictionPlan) drain(view *cacheView, seg segment, want int64, skipPinned bool) int64 {
	if want <= 0 || view.segmentBytes[seg] <= 0 {
		return 0
	}
	if p.selected == nil {
		p.selected = make([]bool, len(view.chunks))
	}
	candidates := view.bySegment[seg]
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
		if skipPinned && chunk.pinned {
			continue
		}
		p.selected[i] = true
		p.victims = append(p.victims, victim{fid: chunk.fid, index: chunk.index, expected: chunk.info})
		freed += chunk.info.size
		view.total -= chunk.info.size
		view.segmentBytes[seg] -= chunk.info.size
		if chunk.pinned {
			view.pinnedBytes -= chunk.info.size
		}
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
