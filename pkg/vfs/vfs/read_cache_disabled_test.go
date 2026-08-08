package vfs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestVFSDisabledReadCacheFullLifecycle: with the read cache disabled, the
// whole open -> read -> edit -> flush -> upload -> read-back lifecycle works.
// Reads miss the cache and hit the driver; uploads proceed and the
// read-cache seed is a no-op; the cache directory stays empty throughout.
func TestVFSDisabledReadCacheFullLifecycle(t *testing.T) {
	driver := drive.NewFakeDriver()
	cacheDir := filepath.Join(t.TempDir(), "reading")
	fs, err := New(driver, Options{
		StorageDir:    t.TempDir(),
		ReadCacheDir:  cacheDir,
		CacheMaxBytes: 0,
		UploadDelay:   5 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fs.Start(ctx)
	defer cancel()

	// Create + write + flush (the edit-and-save path).
	if err := fs.Create(ctx, "/note.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/note.txt", []byte("first version"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/note.txt"); err != nil {
		t.Fatal(err)
	}

	// Open + read back before the upload completes (miss -> driver).
	rc, err := fs.Read(ctx, "/note.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if string(buf) != "first" {
		t.Fatalf("read before upload = %q, want %q", buf, "first")
	}

	// Wait for the upload worker to finish (pending record gone).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := fs.uploads.Store().UploadByPath("/note.txt"); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := fs.uploads.Store().UploadByPath("/note.txt"); ok {
		t.Fatal("upload did not complete with cache disabled")
	}

	// Open + read again after upload: still served from the driver.
	rc2, err := fs.Read(ctx, "/note.txt", 0, 13)
	if err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(rc2)
	rc2.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(all) != "first version" {
		t.Fatalf("read after upload = %q, want %q", all, "first version")
	}

	// Edit + save a second time (uploaded file, not staging).
	if err := fs.Truncate(ctx, "/note.txt", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/note.txt", []byte("edited v2"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/note.txt"); err != nil {
		t.Fatal(err)
	}
	deadline2 := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline2) {
		if _, ok := fs.uploads.Store().UploadByPath("/note.txt"); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rc3, err := fs.Read(ctx, "/note.txt", 0, 9)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := io.ReadAll(rc3)
	rc3.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(v2) != "edited v2" {
		t.Fatalf("read after re-upload = %q, want %q", v2, "edited v2")
	}

	// The cache directory must never have received any chunk data.
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("disabled read cache left file on disk: %s", entry.Name())
	}
	fs.deletes.Close()
	fs.uploads.Close()
	_ = fs.read.Close()
}

// TestVFSDisabledReadCacheDoesNotWriteChunks: a VFS with CacheMaxBytes=0
// serves reads (misses go to the driver) but never persists chunks, and
// the debug snapshot reports the cache as disabled.
func TestVFSDisabledReadCacheDoesNotWriteChunks(t *testing.T) {
	driver := drive.NewFakeDriver()
	cacheDir := filepath.Join(t.TempDir(), "reading")
	fs, err := New(driver, Options{
		StorageDir:    t.TempDir(),
		ReadCacheDir:  cacheDir,
		CacheMaxBytes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := fs.Create(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/a.txt", []byte("hello world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Read(ctx, "/a.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := rc.Read(buf); err != nil {
		t.Fatal(err)
	}
	rc.Close()

	snap := fs.DebugSnapshot()
	found := false
	for _, m := range snap.Mounts {
		if !m.Cache.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatal("debug snapshot did not report disabled read cache")
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("disabled VFS read cache left file on disk: %s", entry.Name())
	}
	_ = fs.read.Close()
}
