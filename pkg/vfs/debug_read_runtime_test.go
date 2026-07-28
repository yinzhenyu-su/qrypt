package vfs

import (
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSDebugReadRuntimeOwnsReadHistoryState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugReadRuntime(fs)

	if got := runtime.NextOpID(); got != "read-1" {
		t.Fatalf("first op id = %q", got)
	}
	for i := 0; i < debugReadHistoryLimit+1; i++ {
		runtime.AppendEvent(drive.MetricEvent{OpID: fmt.Sprintf("event-%d", i)})
	}

	history := runtime.History()
	if len(history) != debugReadHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), debugReadHistoryLimit)
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
	runtime := newVFSDebugReadRuntime(fs)

	const total = debugReadHistoryLimit*3 + 17
	for i := 0; i < total; i++ {
		runtime.AppendEvent(drive.MetricEvent{OpID: fmt.Sprintf("event-%d", i)})
	}
	history := runtime.History()
	if len(history) != debugReadHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(history), debugReadHistoryLimit)
	}
	wantFirst := total - debugReadHistoryLimit
	for i, event := range history {
		want := fmt.Sprintf("event-%d", wantFirst+i)
		if event.OpID != want {
			t.Fatalf("history[%d] = %q, want %q", i, event.OpID, want)
		}
	}
}
