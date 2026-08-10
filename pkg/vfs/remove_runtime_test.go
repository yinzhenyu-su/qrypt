package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSRemoveCleanupRemovesPendingUpload(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(context.Background(), "/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if len(fs.PendingUploads()) != 1 {
		t.Fatalf("pending count = %d, want 1", len(fs.PendingUploads()))
	}
	cleanup := newVFSRemoveCleanup(fs)
	handled, err := cleanup.RemovePendingFile("/pending.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("RemovePendingFile did not handle the pending path")
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}

func TestVFSRemoveCleanupRemovesPendingUploadsUnderDirectory(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Mkdir(context.Background(), "/dir"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(context.Background(), "/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(context.Background(), "/other.txt"); err != nil {
		t.Fatal(err)
	}
	cleanup := newVFSRemoveCleanup(fs)
	if err := cleanup.PrepareDirectory("/dir"); err != nil {
		t.Fatal(err)
	}
	pending := fs.PendingUploads()
	if len(pending) != 1 || pending[0].Path != "/other.txt" {
		t.Fatalf("pending = %+v, want only /other.txt", pending)
	}
}
