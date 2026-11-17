package read

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// --- keys ---

func ChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

func WindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}

// WindowShapeKey keeps single-stream and parallel loads of the same range in
// separate coalescing slots, so an interactive waiter never inherits a bulk
// parallel fetch (and vice versa).
func WindowShapeKey(key string, streams int) string {
	if streams <= 1 {
		return key
	}
	return key + "\x00streams=" + strconv.Itoa(streams)
}

// LoadKey returns the window-coalescing key for an entry: the read cache
// key when available, otherwise the remote id.
func LoadKey(host Host, entry drive.Entry) string {
	if key := host.ReadCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}

// CacheKey computes the read cache key for an entry (stable across mounts
// with the same root id, entry id, size, and mod time).
func CacheKey(rootID string, entry drive.Entry) string {
	if entry.ID == "" || entry.ModTime.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(rootID + "\x00" + entry.ID + "\x00" + strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatInt(entry.ModTime.UTC().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

func DurationMillis(d time.Duration) int64 { return d.Milliseconds() }

func WindowExtra(windowChunks int) map[string]any {
	return map[string]any{
		"window_chunks": windowChunks,
	}
}

// ReadPlanExtra returns the summary-event extra for one read, including the
// resolved intent and stream count when a plan is present.
func ReadPlanExtra(ctx context.Context, windowChunks int) map[string]any {
	extra := WindowExtra(windowChunks)
	if hint, ok := AccessHintFromContext(ctx); ok {
		extra["session_id"], extra["request_id"] = hint.SessionID, hint.RequestID
	}
	if plan, ok := readPlanFromContext(ctx); ok {
		extra["read_intent"] = plan.Intent.String()
		// The access stage explains why this window was chosen. It is the only
		// field that distinguishes a seek's small window from a sequential
		// run's lookahead when the numbers alone look similar.
		extra["read_stage"] = plan.Stage.String()
		extra["read_streams"] = grantedStreams(ctx, plan.MaxStreams)
		if plan.PrefetchChunks > 0 {
			extra["prefetch_chunks"] = plan.PrefetchChunks
		}
		extra["prefetch_mode"] = string(plan.PrefetchMode)
		extra["prefetch_depth"] = plan.PrefetchDepth
		extra["planned_streams"] = plan.MaxStreams
		extra["part_bytes"] = plan.Split.PartBytes
		extra["min_parallel_bytes"] = plan.Split.MinParallelBytes
		extra["cache_writes"] = plan.cacheWrites()
		bounds := fetchTimeoutsFromContext(ctx)
		extra["fetch_idle_timeout_ms"] = bounds.Idle.Milliseconds()
		extra["fetch_total_timeout_ms"] = bounds.Total.Milliseconds()
	}
	return extra
}

// readPlanWindowExtra is the plan slice a per-window detail event carries. The
// full plan (window bytes, split thresholds, timeouts, cache writes) is on the
// summary read event, so a window event only repeats what explains its shape.
func readPlanWindowExtra(ctx context.Context) map[string]any {
	plan, ok := readPlanFromContext(ctx)
	if !ok {
		return nil
	}
	extra := map[string]any{
		"read_intent": plan.Intent.String(),
		// The stage explains why this window was chosen: a slow seek and a slow
		// sequential refill look identical without it.
		"read_stage":     plan.Stage.String(),
		"read_streams":   grantedStreams(ctx, plan.MaxStreams),
		"prefetch_depth": plan.PrefetchDepth,
		"prefetch_mode":  string(plan.PrefetchMode),
	}
	if plan.PrefetchChunks > 0 {
		extra["prefetch_chunks"] = plan.PrefetchChunks
	}
	return extra
}

// isReadFetchTimeout reports whether err represents a fetch timeout, whether
// the read layer produced it or the driver/transport did (for example a
// response-header timeout).
func isReadFetchTimeout(err error) bool {
	if err == nil {
		return false
	}
	return drive.IsFetchTimeout(err) || drive.ErrorCategory(err) == drive.ErrorCategoryTimeout
}

// ReadEventExtra returns the summary-event extra for one read, including the
// wait/fetch timings and the cancel reason when a fetch timeout ended it.
func ReadEventExtra(ctx context.Context, windowChunks int, err error) map[string]any {
	extra := ReadPlanExtra(ctx, windowChunks)
	fetchTimingFromContext(ctx).attach(extra)
	if isReadFetchTimeout(err) {
		extra["cancel_reason"] = "timeout"
	}
	return extra
}

func (r *Reader) newReadTrace(ctx context.Context, entry drive.Entry) *drive.ReadTrace {
	return &drive.ReadTrace{ObserveRange: func(event drive.ReadRangeEvent) {
		phase := "source_range_begin"
		if event.Finished {
			phase = "source_range_end"
		}
		plan, _ := readPlanFromContext(ctx)
		extra := ReadPlanExtra(ctx, max(1, plan.WindowChunks))
		extra["source_id"], extra["range_id"] = event.SourceID, event.ID
		extra["source_in_flight"], extra["speculative"] = event.InFlight, event.Speculative
		RecordChunkDetail(r.observer, ctx, entry, phase, event.Offset/ChunkSize, event.Offset%ChunkSize, event.Size, 0, event.StartedAt, extra, nil)
	}}
}

// ExtraWithWindow returns a copy of a window's trace extra. The window bounds
// are already reported by the event's chunk_index/window_chunks fields, so the
// copy does not repeat them.
func ExtraWithWindow(extra map[string]any) map[string]any {
	merged := CloneExtra(extra)
	if merged == nil {
		merged = map[string]any{}
	}
	return merged
}

func CloneExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}

func shouldPromoteCachedRange(requestSize int64) bool {
	return requestSize > 0 && requestSize < ChunkSize
}

func WindowBytes(chunks map[int64][]byte) int64 {
	var total int64
	for _, chunk := range chunks {
		total += int64(len(chunk))
	}
	return total
}

// DebugOperationName returns the debug op name from ctx, if any.
func DebugOperationName(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Name
}

// RecordChunkDetail records a per-chunk debug event when ctx carries a
// debug operation.
func RecordChunkDetail(observer ReadObserver, ctx context.Context, entry drive.Entry, phase string, index, start, size, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	if hint, ok := AccessHintFromContext(ctx); ok {
		extra["session_id"], extra["request_id"] = hint.SessionID, hint.RequestID
	}
	for key, value := range readPlanWindowExtra(ctx) {
		if _, exists := extra[key]; !exists {
			extra[key] = value
		}
	}
	extra["chunk_index"] = index
	observer.DebugRecordReadDetail(ctx, op.Name, entry.ID, phase, index*ChunkSize+start, size, bytes, started, extra, err)
}

// CacheLookupExtra annotates extra with the cache lookup source and time.
func CacheLookupExtra(source string, started time.Time, extra map[string]any) map[string]any {
	out := CloneExtra(extra)
	if out == nil {
		out = map[string]any{}
	}
	out["cache_lookup_source"] = source
	out["cache_lookup_ms"] = DurationMillis(util.Now().Sub(started))
	return out
}
