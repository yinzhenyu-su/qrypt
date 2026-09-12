package vfs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// Detail events must not consume the summary retention budget: the two
// recorder entry points route to separate rings, so a read's per-chunk
// detail burst cannot evict earlier reads' summaries from the snapshot.
func TestVFSDebugReadHistoryKeepsSummariesUnderDetailFlood(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugReadRuntime(fs.read)
	ctx := drive.WithDebugOperation(context.Background(), drive.DebugOperation{OpID: "read-1", Step: "vfs_read", Name: "/data.bin"})
	started := time.Now()

	diagnostics.RecordRead(runtime, "read-1", "/data.bin", "remote-id", 0, 1<<20, 1<<20, "remote", 2, 3, 2, started, nil, nil)
	for i := 0; i < debugReadDetailHistoryLimit*2+5; i++ {
		diagnostics.RecordReadDetail(runtime, ctx, "/data.bin", "remote-id", "cache_miss_load", int64(i), 0, 1<<20, started, nil, nil)
	}

	history := runtime.History()
	var summaries int
	for _, event := range history {
		if event.Kind == "vfs_read" && event.Phase == "read" {
			summaries++
			if event.Bytes != 1<<20 || event.CacheHits != 2 || event.CacheMisses != 3 {
				t.Fatalf("summary lost its read fields: %+v", event)
			}
			if event.OpID != "read-1" {
				t.Fatalf("summary op id = %q, want read-1", event.OpID)
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("retained read summaries = %d, want 1", summaries)
	}
	if len(history) != 1+debugReadDetailHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), 1+debugReadDetailHistoryLimit)
	}
	// The summary was recorded first, so it must survive at the front of the
	// merged history rather than being buried by the detail flood.
	if history[0].OpID != "read-1" {
		t.Fatalf("history[0] = %q, want the earliest summary read-1", history[0].OpID)
	}
}

func TestVFSDebugReadRuntimeOwnsReadHistoryState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugReadRuntime(fs.read)

	if got := runtime.NextOpID(); got != "read-1" {
		t.Fatalf("first op id = %q", got)
	}
	for i := 0; i < debugReadSummaryHistoryLimit+1; i++ {
		runtime.AppendEvent(drive.MetricEvent{OpID: fmt.Sprintf("event-%d", i)})
	}

	history := runtime.History()
	if len(history) != debugReadSummaryHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), debugReadSummaryHistoryLimit)
	}
	if history[0].OpID != "event-1" {
		t.Fatalf("first retained event = %q", history[0].OpID)
	}
	history[0].OpID = "mutated"
	if runtime.History()[0].OpID == "mutated" {
		t.Fatal("history returned mutable backing storage")
	}

	runtime.ResetHistory()
	if got := runtime.History(); len(got) != 0 {
		t.Fatalf("history after reset = %+v", got)
	}
}

func TestVFSDebugReadRuntimeRingPreservesOrderAcrossWraps(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugReadRuntime(fs.read)

	const total = debugReadSummaryHistoryLimit*3 + 17
	for i := 0; i < total; i++ {
		runtime.AppendEvent(drive.MetricEvent{OpID: fmt.Sprintf("event-%d", i)})
	}
	history := runtime.History()
	if len(history) != debugReadSummaryHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), debugReadSummaryHistoryLimit)
	}
	wantFirst := total - debugReadSummaryHistoryLimit
	for i, event := range history {
		want := fmt.Sprintf("event-%d", wantFirst+i)
		if event.OpID != want {
			t.Fatalf("history[%d] = %q, want %q", i, event.OpID, want)
		}
	}
}
