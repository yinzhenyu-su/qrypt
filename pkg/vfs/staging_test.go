package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStagingCleanupUploadTempsKeepsPendingStaging(t *testing.T) {
	store, err := newStagingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(store.dir, "file.staging")
	remove := filepath.Join(store.dir, "file.staging.upload-123")
	other := filepath.Join(store.dir, "upload-123")
	for _, path := range []string{keep, remove, other} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := store.cleanupUploadTemps(); got != 1 {
		t.Fatalf("cleanupUploadTemps removed %d files, want 1", got)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("pending staging file removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
	if _, err := os.Stat(remove); !os.IsNotExist(err) {
		t.Fatalf("upload temp still exists, err=%v", err)
	}
}

func TestStagingSequentialSmallWritesDoNotUseWholeFilePage(t *testing.T) {
	store, err := newStagingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.create("large")
	if err != nil {
		t.Fatal(err)
	}

	chunk := make([]byte, 16*1024)
	total := int64(4 * 1024 * 1024)
	for off := int64(0); off < total; off += int64(len(chunk)) {
		n, err := store.writeAt(path, chunk, off)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(chunk) {
			t.Fatalf("writeAt wrote %d, want %d", n, len(chunk))
		}
	}

	if _, ok := store.pages.Load("large"); ok {
		t.Fatal("large sequential writes should not keep a whole-file staging page")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != total {
		t.Fatalf("staging size = %d, want %d", info.Size(), total)
	}
}
