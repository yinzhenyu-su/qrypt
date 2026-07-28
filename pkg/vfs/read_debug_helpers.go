package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"time"
)

func readWindowExtra(windowChunks int) map[string]any {
	return map[string]any{
		"window_chunks": windowChunks,
	}
}
func readCacheLookupExtra(source string, started time.Time, extra map[string]any) map[string]any {
	out := cloneReadExtra(extra)
	if out == nil {
		out = map[string]any{}
	}
	out["cache_lookup_source"] = source
	out["cache_lookup_ms"] = durationMillis(timeutil.Now().Sub(started))
	return out
}
func (v *VFS) recordReadChunkDetail(ctx context.Context, entry drive.Entry, phase string, index, start, size, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["chunk_index"] = index
	extra["chunk_offset"] = index * readChunkSize
	extra["chunk_range_start"] = start
	extra["chunk_range_size"] = size
	v.recordDebugReadDetail(ctx, op.Name, entry.ID, phase, index*readChunkSize+start, size, bytes, started, extra, err)
}
func debugOperationName(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Name
}
func cloneReadExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}
func readExtraWithWindow(extra map[string]any, start, end int64) map[string]any {
	merged := cloneReadExtra(extra)
	if merged == nil {
		merged = map[string]any{}
	}
	merged["window_start"] = start
	merged["window_end"] = end
	return merged
}
