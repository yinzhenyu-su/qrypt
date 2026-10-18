package vfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// TestDiscardUploadDropsPendingWithoutTouchingRemote pins the abort path used
// by an offline transfer that failed before finalization: the pending staging
// generation disappears, while an existing remote entry survives untouched.
func TestDiscardUploadDropsPendingWithoutTouchingRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "keep.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := New(localfs.New(remote), Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		UploadDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Create(ctx, "/keep.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/keep.txt", []byte("partial"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/keep.txt"); err != nil {
		t.Fatal(err)
	}
	if pending := fs.PendingUploads(); len(pending) != 1 {
		t.Fatalf("pending uploads = %d, want 1", len(pending))
	}

	if err := fs.DiscardUpload("/keep.txt"); err != nil {
		t.Fatalf("DiscardUpload: %v", err)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending uploads after discard = %+v, want none", pending)
	}
	data, err := os.ReadFile(filepath.Join(remote, "keep.txt"))
	if err != nil || string(data) != "remote" {
		t.Fatalf("remote entry = %q err=%v, want the untouched original", data, err)
	}
}
