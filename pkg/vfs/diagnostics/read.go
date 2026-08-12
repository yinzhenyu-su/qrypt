package diagnostics

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ReadRuntime is the read-event observation surface (consumer side): the
// mount's bounded read-event history. pkg/vfs implements it over the
// internal read domain state.
type ReadRuntime interface {
	AppendEvent(event drive.MetricEvent)
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
}

// DurationMillis converts a duration to whole milliseconds.
func DurationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}
