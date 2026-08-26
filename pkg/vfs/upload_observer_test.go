package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"testing"
)

func TestVFSUploadObserverRecordsSnapshotLifecycle(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := newVFSUploadObserver(fs.uploads, fs.healthTracker)
	pending := PendingUpload{
		FID:      "fid",
		Path:     "/observer.txt",
		Name:     "observer.txt",
		ParentID: "root",
		Size:     10,
	}

	observer.Start(pending)
	observer.State(pending.Path, upload.SnapshotStateUploading)
	observer.Metadata(pending.Path, "remote-id", []string{"sha256"})
	observer.Extra(pending.Path, "local_path", "/tmp/observer")
	observer.Uploaded(pending.Path, 4)
	observer.Finish(pending.Path, upload.SnapshotStateCompleted, "")

	history := fs.DebugSnapshot().Mounts[0].HistoricalUploads()
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one upload", history)
	}
	got := history[0]
	if got.Path != pending.Path || got.State != upload.SnapshotStateCompleted || got.ResultRemoteID != "remote-id" || got.BytesUploaded != 4 {
		t.Fatalf("history upload = %+v, want observer metadata", got)
	}
	if len(got.Hashes) != 1 || got.Hashes[0] != "sha256" || got.Extra["local_path"] != "/tmp/observer" {
		t.Fatalf("history upload metadata = %+v, want hashes and extra", got)
	}
}
