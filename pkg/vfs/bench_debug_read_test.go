package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Benchmarks for the always-on read debug instrumentation.
//
// recordDebugRead / recordDebugReadDetail build a drive.MetricEvent per read
// (and per chunk) and append it to readHistory. Once the history exceeds
// debugReadHistoryLimit, AppendEvent re-copies the entire tail slice on every
// event (O(limit) per append) under a global mutex shared by all reads.

func benchReadHistoryVFS(prefill int) *VFS {
	v := &VFS{read: &readState{history: newReadHistoryState()}}
	if prefill > 0 {
		v.read.history.events = make([]drive.MetricEvent, prefill)
		v.read.history.count = prefill
		v.read.history.pos = 0 // ring is full; next write wraps to slot 0
	}
	return v
}

// AppendEvent on an empty history: append-only cost.
func BenchmarkReadHistoryAppendEmpty(b *testing.B) {
	r := newVFSDebugReadRuntime(benchReadHistoryVFS(0))
	event := drive.MetricEvent{Kind: "vfs_read", Operation: "read", Phase: "read", Path: "/data.bin", Bytes: 1 << 20}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AppendEvent(event)
	}
}

// AppendEvent once the history already hit the limit: adds the O(n) tail copy.
func BenchmarkReadHistoryAppendOverLimit(b *testing.B) {
	r := newVFSDebugReadRuntime(benchReadHistoryVFS(debugReadHistoryLimit))
	event := drive.MetricEvent{Kind: "vfs_read", Operation: "read", Phase: "read", Path: "/data.bin", Bytes: 1 << 20}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.AppendEvent(event)
	}
}

// Full per-chunk detail recording (recordDebugReadDetail + AppendEvent) as it
// runs on every chunk of every VFS.Read today.
func BenchmarkRecordDebugReadDetail(b *testing.B) {
	v := benchReadHistoryVFS(0)
	ctx := context.Background()
	started := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.recordDebugReadDetail(ctx, "/data.bin", "fid", "fetch_window", 0, 1<<20, 1<<20, started, map[string]any{"chunk_index": int64(0)}, nil)
	}
}
