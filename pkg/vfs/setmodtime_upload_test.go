package vfs_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestSetModTimeAppliesToUpload verifies that a SetModTime issued while the
// file is still pending is honored by the upload: the committed entry must
// carry the requested mtime, not the backend upload time.
func TestSetModTimeAppliesToUpload(t *testing.T) {
	remote := t.TempDir()
	driver := localfs.New(remote)
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := vfs.New(driver, vfs.Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		UploadDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.Start(ctx)
	defer cancel()

	const path = "/f.txt"
	if err := v.Create(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteAt(ctx, path, []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := v.Flush(ctx, path); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1_700_000_000, 0)
	if err := v.SetModTime(ctx, path, fixed); err != nil {
		t.Fatalf("SetModTime while pending: %v", err)
	}

	// Wait for the upload to land on the backend.
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := driver.List(context.Background(), "")
		if err == nil {
			for _, e := range entries {
				if e.Name == "f.txt" {
					goto landed
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("upload did not land")
		}
		time.Sleep(20 * time.Millisecond)
	}
landed:

	entry, err := v.Stat(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.ModTime.Equal(fixed) {
		t.Fatalf("committed mtime = %v, want %v", entry.ModTime, fixed)
	}
	// The backend file must carry the same mtime.
	info, err := driver.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range info {
		if e.Name == "f.txt" && !e.ModTime.Equal(fixed) {
			t.Fatalf("backend mtime = %v, want %v", e.ModTime, fixed)
		}
	}
}
