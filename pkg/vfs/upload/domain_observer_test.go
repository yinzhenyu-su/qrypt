package upload

import "testing"

func TestDomainObserverRecordsSnapshotLifecycle(t *testing.T) {
	t.Parallel()
	debug := NewDebugState()
	observer := domainObserver{debug: debug}
	pending := PendingUpload{
		FID:      "fid",
		Path:     "/observer.txt",
		Name:     "observer.txt",
		ParentID: "root",
		Size:     10,
	}

	observer.Start(pending)
	observer.State(pending.Path, SnapshotStateUploading)
	observer.Metadata(pending.Path, "remote-id", []string{"sha256"})
	observer.Extra(pending.Path, "local_path", "/tmp/observer")
	observer.Uploaded(pending.Path, 4)
	observer.Finish(pending.Path, SnapshotStateCompleted, "")

	history := debug.HistorySnapshots()
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one upload", history)
	}
	got := history[0]
	if got.Path != pending.Path || got.State != SnapshotStateCompleted || got.ResultRemoteID != "remote-id" || got.BytesUploaded != 4 {
		t.Fatalf("history upload = %+v, want observer metadata", got)
	}
	if len(got.Hashes) != 1 || got.Hashes[0] != "sha256" || got.Extra["local_path"] != "/tmp/observer" {
		t.Fatalf("history upload metadata = %+v, want hashes and extra", got)
	}
}
