package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestDebugUploadRuntimeOwnsActiveAndHistoryState(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSDebugUploadRuntime(fs)

	runtime.StartSnapshot(PendingUpload{
		FID:      "op-1",
		Path:     "/file.txt",
		Name:     "file.txt",
		Size:     10,
		ParentID: "parent",
	})
	runtime.UpdateSnapshot("/file.txt", 4)
	active := runtime.ActiveSnapshots()
	if got := active["/file.txt"].BytesUploaded; got != 4 {
		t.Fatalf("active bytes = %d, want 4", got)
	}

	runtime.SetSnapshotMetadata("/file.txt", "remote-id", []string{"sha1:x"})
	runtime.FinishSnapshot("/file.txt", "done", "")
	if active := runtime.ActiveSnapshots(); len(active) != 0 {
		t.Fatalf("active snapshots after finish = %d, want 0", len(active))
	}
	history := runtime.History()
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}
	if history[0].ResultRemoteID != "remote-id" || len(history[0].Hashes) != 1 {
		t.Fatalf("history metadata not recorded: %+v", history[0])
	}
	if !runtime.RemoveHistoryByID("op-1") {
		t.Fatal("expected history record to be removed")
	}
	if history := runtime.History(); len(history) != 0 {
		t.Fatalf("history length after remove = %d, want 0", len(history))
	}
}
