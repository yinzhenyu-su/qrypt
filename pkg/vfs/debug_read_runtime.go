package vfs

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (v *VFS) debugCacheCounters() (hits, misses int64) {
	return newVFSDebugReadRuntime(v).CacheCounters()
}

func (v *VFS) nextDebugReadOpID() string {
	return newVFSDebugReadRuntime(v).NextOpID()
}

func (v *VFS) recordDebugRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordRead(newVFSDebugReadRuntime(v), opID, path, remoteID, offset, requested, bytes, source, cacheHits, cacheMisses, chunks, started, extra, err)
}

func (v *VFS) recordDebugReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	diagnostics.RecordReadDetail(newVFSDebugReadRuntime(v), ctx, path, remoteID, phase, offset, requested, bytes, started, extra, err)
}

func (v *VFS) debugReadHistory() []drive.MetricEvent {
	return newVFSDebugReadRuntime(v).History()
}

func (v *VFS) DebugReset(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newVFSDebugReadRuntime(v).ResetHistory()
	return nil
}

type vfsDebugReadRuntime struct {
	v *VFS
}

func newVFSDebugReadRuntime(v *VFS) vfsDebugReadRuntime {
	return vfsDebugReadRuntime{v: v}
}

func (r vfsDebugReadRuntime) NextOpID() string {
	return fmt.Sprintf("read-%d", r.v.read.NextSequence())
}

func (r vfsDebugReadRuntime) CacheCounters() (hits, misses int64) {
	return r.v.readCacheCounters()
}

func (r vfsDebugReadRuntime) AppendEvent(event drive.MetricEvent) {
	r.v.read.AppendHistory(event)
}

func (r vfsDebugReadRuntime) History() []drive.MetricEvent {
	return r.v.read.HistorySnapshot()
}

func (r vfsDebugReadRuntime) ResetHistory() {
	r.v.read.ResetHistory()
}

// --- migrated from debug_resolve.go ---
