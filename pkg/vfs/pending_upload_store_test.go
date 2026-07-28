package vfs

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestUploadStoreAdapterRecordsPendingState(t *testing.T) {
	store, err := newUploadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := newUploadStoreAdapter(store)
	localPath, err := store.staging.create("file")
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

	replaced, ok, err := adapter.RecordReplacementIfUnchanged(failed, UploadReplacement{ID: "uploaded", Name: temporaryUploadName("file.txt", "file"), Size: 5})
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
	store, err := newUploadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := newUploadStoreAdapter(store)
	localPath, err := store.staging.create("file")
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

	otherPath, err := store.staging.create("other")
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
