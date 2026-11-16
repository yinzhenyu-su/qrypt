package diagnostics

import (
	"context"
	"errors"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadRuntime is the read-event observation surface (consumer side): the
// mount's bounded read-event history. pkg/vfs implements it over the
// internal read domain state.
//
// Summary and detail events are appended through separate methods so the
// retention split is explicit at the call site: summaries are one per read
// and must not be evicted by the unbounded per-chunk detail stream.
type ReadRuntime interface {
	AppendEvent(event drive.MetricEvent)
	AppendDetailEvent(event drive.MetricEvent)
	History() []drive.MetricEvent
	ResetHistory()
}

// RecordRead appends one completed vfs_read metric event (the read path's
// debug observer). Pure orchestration: no VFS internals.
func RecordRead(runtime ReadRuntime, opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	finished := util.Now()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["source"] = source
	event := drive.MetricEvent{
		At: finished, OpID: opID, Kind: "vfs_read", Operation: "read", Phase: "read", State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, CacheHits: cacheHits, CacheMisses: cacheMisses, Chunks: chunks,
		StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: DurationMillis(finished.Sub(started)),
		Extra: extra,
	}
	applyReadPolicyFields(&event, extra)
	if bytes > 0 && finished.After(started) {
		event.Throughput = int64(float64(bytes) / finished.Sub(started).Seconds())
	}
	if err != nil {
		event.State = "failed"
		event.OK = false
		event.Error = err.Error()
		event.ErrorCategory = drive.ErrorCategory(err)
	}
	runtime.AppendEvent(event)
	stage, _ := extra["read_stage"].(string)
	fetchMicros, _ := extra["fetch_us"].(int64)
	waitMicros, _ := extra["wait_us"].(int64)
	if (stage == "initial" || stage == "seek") && (fetchMicros > 0 || waitMicros > 0 || err != nil) {
		logging.L.Debugf("[READ] op=%s phase=read offset=%d requested=%d bytes=%d duration_us=%d state=%s extra=%v", opID, offset, requested, bytes, finished.Sub(started).Microseconds(), event.State, extra)
	}
}

// RecordReadDetail appends one phase-level vfs_read metric event scoped
// under a parent debug operation from the context.
func RecordReadDetail(runtime ReadRuntime, ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	finished := util.Now()
	event := drive.MetricEvent{
		At: finished, ParentOpID: op.OpID, Kind: "vfs_read", Operation: "read", Phase: phase, State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: DurationMillis(finished.Sub(started)),
		Extra: extra,
	}
	applyReadPolicyFields(&event, extra)
	if bytes > 0 && finished.After(started) {
		event.Throughput = int64(float64(bytes) / finished.Sub(started).Seconds())
	}
	if err != nil {
		if errors.Is(err, context.Canceled) && event.PrefetchCancelReason != "" {
			// A canceled background prefetch is a normal outcome, not a read
			// failure. Foreground cancellation keeps the failed state.
			event.State = "canceled"
			event.OK = true
			event.Error = err.Error()
			event.ErrorCategory = drive.ErrorCategory(err)
		} else {
			event.State = "failed"
			event.OK = false
			event.Error = err.Error()
			event.ErrorCategory = drive.ErrorCategory(err)
		}
	}
	runtime.AppendDetailEvent(event)
	switch phase {
	case "read_access", "prefetch_plan", "source_range_begin", "source_range_end", "wait_window_begin", "wait_window_done", "wait_window_bypass", "wait_window_load_begin", "wait_window_load_bypass", "wait_window_load", "fetch_window", "prefetch_window":
		// Keep these causal events in the debug log as well as the bounded
		// history: small FUSE reads can evict the start of a slow seek.
		// Extras contain read plans and timings, never provider URLs/headers.
		logging.L.Debugf("[READ] op=%s phase=%s offset=%d requested=%d bytes=%d duration_us=%d state=%s extra=%v", op.OpID, phase, offset, requested, bytes, finished.Sub(started).Microseconds(), event.State, extra)
	}
}

func applyReadPolicyFields(event *drive.MetricEvent, extra map[string]any) {
	if value, ok := extra["read_intent"].(string); ok {
		event.ReadIntent = value
	}
	if value, ok := extra["read_streams"].(int); ok {
		event.ReadStreams = value
	}
	if value, ok := extra["prefetch_cancel_reason"].(string); ok {
		event.PrefetchCancelReason = value
	}
}

// DurationMillis converts a duration to whole milliseconds.
func DurationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}
