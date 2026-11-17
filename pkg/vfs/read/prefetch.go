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
	if logging.L.Enabled(logging.LevelDebug) {
		defer func() { r.recordPrefetchPlan(ctx, entry, cursor, horizonEnd, reason) }()
	}
	owned := r.prefetchInFlight(cacheKey)
	if owned >= depth {
		reason = "depth"
		return
	}
	for index := cursor; index < horizonEnd; {
		if r.readChunkAvailableFor(cacheKey, index, 0) {
			index++
			continue
		}
		count := int(min(int64(window), horizonEnd-index))
		load := r.startPrefetchWindow(ctx, entry, index, count, true)
		if load == nil {
			reason = "not_admitted"
			return
		}
		owned++
		index = load.end + 1
		if owned >= depth {
			reason = "depth"
			return
		}
	}
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
	return r.state.findWindow(cacheKey, index, 0, 0) != nil
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
		"prefetch_mode":   string(plan.PrefetchMode),
		"source_id":       r.source.Policy().SourceID,
		"source_admitted": stats.NormalInUse + stats.HighInUse,
		"object_admitted": r.state.scheduler.PerFileInUse(entry.ID),
		"prefetch_in_use": stats.PrefetchInUse,
	}
	RecordChunkDetail(r.observer, ctx, entry, "prefetch_plan", cursor, 0, (end-cursor)*ChunkSize, (readyEnd-cursor)*ChunkSize, time.Now(), extra, nil)
}
