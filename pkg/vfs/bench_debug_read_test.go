package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/read"
)

// Benchmarks for the always-on read debug instrumentation.
//
// recordDebugRead / recordDebugReadDetail build a drive.MetricEvent per read
// (and per chunk) and append it to readHistory. Once the history exceeds
// debugReadHistoryLimit, AppendEvent re-copies the entire tail slice on every
// event (O(limit) per append) under a global mutex shared by all reads.

func benchReadHistoryVFS(prefill int) *VFS {
	v := &VFS{read: read.NewState(nil)}
	if prefill > 0 {
		for i := 0; i < prefill; i++ {
			v.read.AppendHistory(drive.MetricEvent{Kind: "vfs_read", Operation: "read", Phase: "read", Path: "/data.bin", Bytes: 1 << 20})
		}
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
	r := newVFSDebugReadRuntime(benchReadHistoryVFS(read.HistoryLimit))
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
