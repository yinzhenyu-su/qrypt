package vfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestReadCacheDisabledStoreShortCircuits: a store constructed with
// max_size <= 0 must never write chunks, never report hits, and must be
// safe to flush/clear/close (the write queue and index are absent).
func TestReadCacheDisabledStoreShortCircuits(t *testing.T) {
	dir := t.TempDir()
	store, err := newReadCacheStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if store.enabled() {
		t.Fatal("max_size=0 store reported enabled")
	}

	store.PutChunkAsync("fid", 64, 0, []byte("data"))
	if err := store.PutChunk("fid", 64, 0, []byte("data")); err != nil {
		t.Fatalf("PutChunk on disabled store: %v", err)
	}
	if err := store.PutLocalFile("fid", 64, t.TempDir()); err != nil {
		t.Fatalf("PutLocalFile on disabled store: %v", err)
	}
	if ok, err := store.HasChunk("fid", 0); err != nil || ok {
		t.Fatalf("HasChunk on disabled store = %v,%v want miss", ok, err)
	}
	if data, ok, err := store.GetChunk("fid", 0); err != nil || ok || data != nil {
		t.Fatalf("GetChunk on disabled store = %v,%v want miss", ok, err)
	}
	if _, _, ok, err := store.GetChunkWithRange("fid", 0, 0, 4); err != nil || ok {
		t.Fatalf("GetChunkWithRange on disabled store = %v,%v want miss", ok, err)
	}
	if err := store.FlushReadCache(); err != nil {
		t.Fatalf("FlushReadCache on disabled store: %v", err)
	}
	if err := store.ClearReadCache(); err != nil {
		t.Fatalf("ClearReadCache on disabled store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close on disabled store: %v", err)
	}

	// No chunk data may land on disk.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("disabled read cache left file on disk: %s", entry.Name())
	}
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
