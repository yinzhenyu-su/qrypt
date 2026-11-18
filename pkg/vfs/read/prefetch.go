package read

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// schedulePrefetch keeps a fixed W×D horizon of speculative windows ahead of
// the read cursor. Reads drive it: every foreground read refreshes the horizon
// and admits whatever is missing, nearest the cursor first. There is no
// per-handle worker or idle timer, so a paused reader stops growing its horizon
// and the in-flight windows retire with their scope.
func (r *Reader) schedulePrefetch(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int, sequential bool, plan ReadPlan) {
	if foregroundSplitEnabled() && r.foregroundSplitting(entry.ID) {
		// Adaptive foreground split: while the foreground fetches its own
		// parallel parts, speculative lookahead would only compete for the same
		// per-object budget and connections.
		return
	}
	if sequential && plan.Stage != StageSequential {
		plan = r.resolvePlan(ctx, windowChunks, StageSequential)
	}
	window, depth := max(1, plan.PrefetchChunks), max(1, plan.PrefetchDepth)
	cursor := endChunk + 1
	if entry.Size <= 0 || window <= 0 || depth <= 0 {
		return
	}
	fileEnd := (entry.Size-1)/ChunkSize + 1
	horizonEnd := min(cursor+int64(window*depth), fileEnd)
	if cursor >= horizonEnd {
		return
	}
	cacheKey := r.host.ReadCacheKey(entry)
	if cacheKey == "" {
		return
	}
	reason := "covered"
	started := 0
	defer func() {
		if logging.L.Enabled(logging.LevelDebug) {
			r.logPrefetchPlan(ctx, entry, cacheKey, cursor, horizonEnd, reason, started)
		}
	}()
	owned := r.prefetchInFlight(cacheKey)
	if owned >= depth {
		reason = "depth"
		return
	}
	for index := cursor; index < horizonEnd; {
		// A window already in the registry will deliver index; admitting a
		// second window over the same range would download those bytes twice.
		// This must look at every session's windows: the read-side join rule
		// deliberately avoids waiting on another session's stall, which would
		// hide the window from scheduling too.
		covered, end, next := r.windowCoverage(cacheKey, index)
		if covered {
			index = end + 1
			continue
		}
		if r.readChunkAvailableFor(cacheKey, index, 0) {
			index++
			continue
		}
		count := int(min(int64(window), horizonEnd-index))
		if next > index && next < horizonEnd {
			// Do not straddle a window another read already admitted.
			count = int(min(int64(count), next-index))
		}
		load := r.startPrefetchWindow(ctx, entry, index, count, true)
		if load == nil {
			reason = "not_admitted"
			return
		}
		started++
		owned++
		index = load.end + 1
		if owned >= depth {
			reason = "depth"
			return
		}
	}
}

// logPrefetchPlan emits the per-read scheduling decision, but only when the
// read admitted speculative work or the stop reason changed. A horizon refresh
// happens on every foreground read, so logging unconditionally would emit one
// line per 16 KiB read; one line per admitted window keeps the diagnostic
// useful without turning debug logging into the bottleneck it observes.
func (r *Reader) logPrefetchPlan(ctx context.Context, entry drive.Entry, cacheKey string, cursor, end int64, reason string, started int) {
	r.prefetchLogMu.Lock()
	if r.prefetchLog == nil {
		r.prefetchLog = make(map[string]string, 8)
	}
	last, seen := r.prefetchLog[cacheKey]
	if started == 0 && seen && last == reason {
		r.prefetchLogMu.Unlock()
		return
	}
	if len(r.prefetchLog) > 1024 {
		r.prefetchLog = make(map[string]string, 8)
	}
	r.prefetchLog[cacheKey] = reason
	r.prefetchLogMu.Unlock()
	r.recordPrefetchPlan(ctx, entry, cursor, end, reason)
}

// prefetchInFlight counts speculative windows already admitted for one file.
// The plan's depth bounds them, independent of how far the cursor has moved.
func (r *Reader) prefetchInFlight(cacheKey string) int {
	r.state.windows.mu.Lock()
	defer r.state.windows.mu.Unlock()
	owned := 0
	for _, load := range r.state.windows.loads {
		if load.prefetch && load.fid == cacheKey {
			owned++
		}
	}
	return owned
}

// windowCoverage reports how the windows already in the registry relate to
// index: covered is true when one of them will deliver index (end is the last
// chunk of the furthest such window), otherwise next is the first chunk of a
// later window, or -1. Scheduling scans every session's windows, because the
// read-side join rule intentionally ignores another session's stalled
// lookahead; that rule must not hide in-flight bytes from the scheduler, or
// two windows fetch the same range.
func (r *Reader) windowCoverage(cacheKey string, index int64) (covered bool, end, next int64) {
	r.state.windows.mu.Lock()
	defer r.state.windows.mu.Unlock()
	end, next = -1, -1
	for _, load := range r.state.windows.loads {
		if load.fid != cacheKey {
			continue
		}
		if index >= load.start && index <= load.end {
			covered = true
			if load.end > end {
				end = load.end
			}
			continue
		}
		if load.start > index && (next < 0 || load.start < next) {
			next = load.start
		}
	}
	return covered, end, next
}

// prefetchResident reports whether index is resident without touching the hot
// LRU or admitting anything; diagnostics must not change read behavior.
func (r *Reader) prefetchResident(cacheKey string, index int64) bool {
	if r.state.peekHotChunk(cacheKey, index) {
		return true
	}
	if r.state.cache != nil {
		if ok, err := r.state.cache.HasChunk(cacheKey, index); err != nil || ok {
			return true
		}
	}
	return r.state.findWindow(cacheKey, index, 0, 0, 0) != nil
}

// recordPrefetchPlan snapshots the planned horizon for diagnostics only. It
// reports what the scheduler is about to admit; readiness is read from the hot
// cache, durable cache and window registry without disturbing them.
func (r *Reader) recordPrefetchPlan(ctx context.Context, entry drive.Entry, cursor, end int64, reason string) {
	cacheKey := r.host.ReadCacheKey(entry)
	readyEnd := cursor
	for readyEnd < end && r.prefetchResident(cacheKey, readyEnd) {
		readyEnd++
	}
	plan, _ := readPlanFromContext(ctx)
	stats := r.state.scheduler.Stats()
	pending := r.prefetchInFlight(cacheKey)
	extra := map[string]any{
		"cursor_chunk": cursor, "horizon_end_chunk": end, "ready_end_chunk": readyEnd,
		"pending_windows": pending, "fill_stop": reason,
		"prefetch_window_chunks": max(1, plan.PrefetchChunks), "prefetch_depth": max(1, plan.PrefetchDepth),
		"source_id":       r.source.Policy().SourceID,
		"source_admitted": stats.NormalInUse + stats.HighInUse,
		"object_admitted": r.state.scheduler.PerFileInUse(entry.ID),
		"prefetch_in_use": stats.PrefetchInUse,
	}
	RecordChunkDetail(r.observer, ctx, entry, "prefetch_plan", cursor, 0, (end-cursor)*ChunkSize, (readyEnd-cursor)*ChunkSize, time.Now(), extra, nil)
}
