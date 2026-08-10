package upload

import (
	"errors"
	"os"
	"testing"
)

func TestUploadStoreWriteAdapterStagesAndRecordsUpload(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := store
	localPath, err := adapter.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := adapter.WriteStagingAt(localPath, []byte("hello"), 0); err != nil || n != len("hello") {
		t.Fatalf("WriteStagingAt n=%d err=%v", n, err)
	}
	if err := adapter.FlushStaging(localPath); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SyncStaging(localPath); err != nil {
		t.Fatal(err)
	}
	size, err := adapter.StagingSize(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len("hello")) {
		t.Fatalf("staging size = %d, want %d", size, len("hello"))
	}
	pending := PendingUpload{Path: "/file.txt", FID: "file", LocalPath: localPath, Size: size}
	if err := adapter.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	if got, ok := adapter.UploadByPath(pending.Path); !ok || got.LocalPath != localPath {
		t.Fatalf("pending = %+v, ok=%v", got, ok)
	}
	pending.Size = 10
	adapter.UpdateUploadTransient(pending)
	if got, ok := adapter.UploadByPath(pending.Path); !ok || got.Size != 10 {
		t.Fatalf("updated pending = %+v, ok=%v", got, ok)
	}

	if err := adapter.TruncateStaging(localPath, 2); err != nil {
		t.Fatal(err)
	}
	size, err = adapter.StagingSize(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if size != 2 {
		t.Fatalf("truncated staging size = %d, want 2", size)
	}
}

func TestUploadStoreWriteAdapterRemovesUnreferencedStaging(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := store
	localPath, err := adapter.CreateStaging("file")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.SaveUpload(PendingUpload{Path: "/file.txt", FID: "file", LocalPath: localPath}); err != nil {
		t.Fatal(err)
	}
	adapter.RemoveStagingIfUnreferenced(localPath)
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("referenced staging removed: %v", err)
	}
	if err := store.RemoveUpload("/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging should be removed with pending upload: %v", err)
	}

	orphanPath, err := adapter.CreateStaging("orphan")
	if err != nil {
		t.Fatal(err)
	}
	adapter.RemoveStagingIfUnreferenced(orphanPath)
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan staging still exists: %v", err)
	}
}
