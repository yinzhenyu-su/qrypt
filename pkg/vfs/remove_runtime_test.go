package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSRemoveRuntimeRemovesPendingUpload(t *testing.T) {
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
	runtime := newVFSRemoveRuntime(fs)
	if err := runtime.RemovePendingUpload("/pending.txt"); err != nil {
		t.Fatal(err)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
}

func TestVFSRemoveRuntimeRemovesPendingUploadsUnderDirectory(t *testing.T) {
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
	runtime := newVFSRemoveRuntime(fs)
	if err := runtime.RemovePendingUploadsUnder("/dir"); err != nil {
		t.Fatal(err)
	}
	pending := fs.PendingUploads()
	if len(pending) != 1 || pending[0].Path != "/other.txt" {
		t.Fatalf("pending = %+v, want only /other.txt", pending)
	}
}
