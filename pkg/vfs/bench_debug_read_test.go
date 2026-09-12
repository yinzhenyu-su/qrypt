package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

// Benchmarks for the always-on read debug instrumentation.
//
// recordDebugRead / recordDebugReadDetail build a drive.MetricEvent per read
// (and per chunk) and append it to the read history. Appends are O(1): the
// ring writes into a slot and only copies while doubling toward the limit,
// so the per-event cost does not grow once the ring is full.

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
	r := newVFSDebugReadRuntime(benchReadHistoryVFS(0).read)
	event := drive.MetricEvent{Kind: "vfs_read", Operation: "read", Phase: "read", Path: "/data.bin", Bytes: 1 << 20}
	b.ReportAllocs()

	for b.Loop() {
		r.AppendEvent(event)
	}
}

// AppendEvent once the history already hit the limit: steady-state cost.
func BenchmarkReadHistoryAppendOverLimit(b *testing.B) {
	r := newVFSDebugReadRuntime(benchReadHistoryVFS(read.SummaryHistoryLimit).read)
	event := drive.MetricEvent{Kind: "vfs_read", Operation: "read", Phase: "read", Path: "/data.bin", Bytes: 1 << 20}
	b.ReportAllocs()

	for b.Loop() {
		r.AppendEvent(event)
	}
}

// Full per-chunk detail recording (recordDebugReadDetail + AppendEvent) as it
// runs on every chunk of every VFS.Read today.
func BenchmarkRecordDebugReadDetail(b *testing.B) {
	v := benchReadHistoryVFS(0)
	runtime := newVFSDebugReadRuntime(v.read)
	ctx := context.Background()
	started := time.Now()
	b.ReportAllocs()

	for b.Loop() {
		diagnostics.RecordReadDetail(runtime, ctx, "/data.bin", "fid", "fetch_window", 0, 1<<20, 1<<20, started, map[string]any{"chunk_index": int64(0)}, nil)
	}
}
