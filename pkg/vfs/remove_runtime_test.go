package vfs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSRemoveCleanupRemovesPendingUpload(t *testing.T) {
	t.Parallel()
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
	handled, _, err := cleanup.RemovePendingFile(context.Background(), "/pending.txt")
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

func TestVFSRemoveCleanupRejectsPendingUploadsUnderDirectory(t *testing.T) {
	t.Parallel()
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
	if err := cleanup.PrepareDirectory("/dir"); !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("PrepareDirectory err = %v, want ErrDirNotEmpty", err)
	}
	pending := fs.PendingUploads()
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want both uploads preserved", pending)
	}
}
