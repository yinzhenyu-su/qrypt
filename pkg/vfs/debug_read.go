package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"sync/atomic"
	"time"
)

func (v *VFS) debugCacheCounters() (hits, misses int64) {
	return newVFSDebugReadRuntime(v).CacheCounters()
}

func (v *VFS) nextDebugReadOpID() string {
	return newVFSDebugReadRuntime(v).NextOpID()
}

func (v *VFS) recordDebugRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, extra map[string]any, err error) {
	finished := timeutil.Now()
	if extra == nil {
		extra = map[string]any{}
	}
	extra["source"] = source
	event := drive.MetricEvent{
		At: finished, OpID: opID, Kind: "vfs_read", Operation: "read", Phase: "read", State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, CacheHits: cacheHits, CacheMisses: cacheMisses, Chunks: chunks,
		StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: durationMillis(finished.Sub(started)),
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
	newVFSDebugReadRuntime(v).AppendEvent(event)
}

func (v *VFS) recordDebugReadDetail(ctx context.Context, path, remoteID, phase string, offset, requested, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	finished := timeutil.Now()
	event := drive.MetricEvent{
		At: finished, ParentOpID: op.OpID, Kind: "vfs_read", Operation: "read", Phase: phase, State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: durationMillis(finished.Sub(started)),
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
	newVFSDebugReadRuntime(v).AppendEvent(event)
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

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

type vfsDebugReadRuntime struct {
	v *VFS
}

func newVFSDebugReadRuntime(v *VFS) vfsDebugReadRuntime {
	return vfsDebugReadRuntime{v: v}
}

func (r vfsDebugReadRuntime) NextOpID() string {
	return fmt.Sprintf("read-%d", atomic.AddUint64(&r.v.read.history.sequence, 1))
}

func (r vfsDebugReadRuntime) CacheCounters() (hits, misses int64) {
	return r.v.read.cache.stats.hits.Load(), r.v.read.cache.stats.misses.Load()
}

func (r vfsDebugReadRuntime) AppendEvent(event drive.MetricEvent) {
	r.v.read.history.mu.Lock()
	h := r.v.read.history
	if h.events == nil {
		// Grow lazily toward the limit so idle VFS instances do not
		// preallocate a full ring.
		size := 64
		if debugReadHistoryLimit < size {
			size = debugReadHistoryLimit
		}
		h.events = make([]drive.MetricEvent, size)
	}
	if h.count == len(h.events) {
		if len(h.events) < debugReadHistoryLimit {
			// Ring full but not at the limit yet: double, preserving order.
			size := len(h.events) * 2
			if size > debugReadHistoryLimit {
				size = debugReadHistoryLimit
			}
			next := make([]drive.MetricEvent, size)
			for i := 0; i < h.count; i++ {
				next[i] = h.events[(h.pos-h.count+i+len(h.events))%len(h.events)]
			}
			h.events = next
			h.pos = h.count
		}
	}
	h.events[h.pos] = event
	h.pos = (h.pos + 1) % len(h.events)
	if h.count < len(h.events) {
		h.count++
	}
	r.v.read.history.mu.Unlock()
}

func (r vfsDebugReadRuntime) History() []drive.MetricEvent {
	r.v.read.history.mu.Lock()
	defer r.v.read.history.mu.Unlock()
	h := r.v.read.history
	if h.count == 0 {
		return nil
	}
	out := make([]drive.MetricEvent, h.count)
	for i := 0; i < h.count; i++ {
		out[i] = h.events[(h.pos-h.count+i+len(h.events))%len(h.events)]
	}
	return out
}

func (r vfsDebugReadRuntime) ResetHistory() {
	r.v.read.history.mu.Lock()
	r.v.read.history.events = nil
	r.v.read.history.pos = 0
	r.v.read.history.count = 0
	r.v.read.history.mu.Unlock()
}
