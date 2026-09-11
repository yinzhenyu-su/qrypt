package upload

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestUploadStoreAdapterRecordsPendingState(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(store)
	localPath, err := store.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:      "/file.txt",
		FID:       "file",
		ParentID:  "root",
		Name:      "file.txt",
		LocalPath: localPath,
		Size:      5,
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, ok := adapter.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending upload not found")
	}

	failed, ok, err := adapter.RecordFailureIfUnchanged(current, errors.New("temporary failure"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || failed.RetryCount != 1 || failed.LastError != "temporary failure" || failed.NextAttemptAt == 0 {
		t.Fatalf("failed pending = %+v, ok=%v", failed, ok)
	}
	stale := failed
	stale.Size++
	if _, ok, err := adapter.RecordReplacementIfUnchanged(stale, UploadReplacement{ID: "uploaded"}); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("stale pending unexpectedly updated")
	}

	replaced, ok, err := adapter.RecordReplacementIfUnchanged(failed, UploadReplacement{ID: "uploaded", Name: TemporaryUploadName("file.txt", "file"), Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || replaced.ReplaceUpload == nil || replaced.ReplaceUpload.ID != "uploaded" {
		t.Fatalf("replaced pending = %+v, ok=%v", replaced, ok)
	}
	if removed, err := adapter.RemoveIfUnchanged(stale); err != nil {
		t.Fatal(err)
	} else if removed {
		t.Fatal("stale pending unexpectedly removed")
	}
	if removed, err := adapter.RemoveIfUnchanged(replaced); err != nil {
		t.Fatal(err)
	} else if !removed {
		t.Fatal("current pending not removed")
	}
}

func TestUploadStoreAdapterCleansStaging(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewStoreAdapter(store)
	localPath, err := store.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{Path: "/file.txt", FID: "file", LocalPath: localPath}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}

	adapter.RemoveStagingIfUnreferenced(localPath)
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("referenced staging removed: %v", err)
	}
	current, ok := adapter.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending upload not found")
	}
	if removed, err := adapter.RemoveIfUnchanged(current); err != nil {
		t.Fatal(err)
	} else if !removed {
		t.Fatal("pending not removed")
	}
	adapter.RemoveStagingIfUnreferenced(localPath)
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced staging still exists: %v", err)
	}

	otherPath, err := store.CreateStaging("other")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveStaging(otherPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(otherPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging still exists after direct remove: %v", err)
	}
}

// TestRebaseUploadsUnderMovesChildrenAndReparents: a renamed directory takes
// its pending descendants with it - path, recorded parent, and journal state -
// while records outside the subtree stay untouched.
func TestRebaseUploadsUnderMovesChildrenAndReparents(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	child := PendingUpload{Path: "/dir/sub/f.txt", FID: "child", Name: "f.txt", ParentID: "old-sub", Frozen: true}
	root := PendingUpload{Path: "/dir/g.txt", FID: "root", Name: "g.txt", ParentID: "old-dir", Frozen: true}
	other := PendingUpload{Path: "/other.txt", FID: "other", Name: "other.txt", ParentID: "old-dir", Frozen: true}
	for _, pending := range []PendingUpload{child, root, other} {
		pending.LocalPath = stagingFor(t, store, pending.FID)
		pending.Size = 1
		if err := store.SaveUploadExact(pending); err != nil {
			t.Fatal(err)
		}
	}

	moved, err := store.RebaseUploadsUnder("/dir", "/moved", "new-dir-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 {
		t.Fatalf("rebased = %+v, want the two records under /dir", moved)
	}
	for _, want := range []struct{ path, fid string }{{"/moved/g.txt", "root"}, {"/moved/sub/f.txt", "child"}} {
		got, ok := store.UploadByPath(want.path)
		if !ok {
			t.Fatalf("rebased record %q missing", want.path)
		}
		if got.FID != want.fid || got.ParentID != "new-dir-id" || got.LocalPath == "" {
			t.Fatalf("rebased record = %+v, want fid %q re-parented to new-dir-id with its staging", got, want.fid)
		}
		if _, ok := store.PendingByID(want.fid); !ok {
			t.Fatalf("id index lost %q", want.fid)
		}
	}
	if _, ok := store.UploadByPath("/dir/g.txt"); ok {
		t.Fatal("old path still present after rebase")
	}
	if untouched, ok := store.UploadByPath("/other.txt"); !ok || untouched.ParentID != "old-dir" {
		t.Fatalf("unrelated record = %+v ok=%v, want it untouched", untouched, ok)
	}

	// The rebase must survive a replay: reopen the store over the same dir.
	reopened, err := NewPendingStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.UploadByPath("/moved/sub/f.txt"); !ok || got.FID != "child" {
		t.Fatalf("replayed record = %+v ok=%v, want the rebased child", got, ok)
	}
	if _, ok := reopened.UploadByPath("/dir/sub/f.txt"); ok {
		t.Fatal("replay resurrected the pre-rebase path")
	}
}

// TestRebaseUploadsUnderNoMatchIsNoop: a directory without pending uploads
// rebases nothing and writes nothing.
// stagingFor creates a real staging file for a record, so the durable journal
// keeps the entry when it is replayed.
func stagingFor(t *testing.T, store *PendingStore, fid string) string {
	t.Helper()
	path, err := store.CreateStaging(fid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRebaseUploadsUnderNoMatchIsNoop(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	moved, err := store.RebaseUploadsUnder("/empty", "/gone", "id")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 0 {
		t.Fatalf("rebased = %+v, want none", moved)
	}
}
