package vfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSUploadRuntimeAppliesModTimeAndCommitsEntry(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newVFSUploadRuntime(fs)
	modTime := time.Unix(1234, 0)
	pending := PendingUpload{
		Path:    "/uploaded.txt",
		Name:    "uploaded.txt",
		ModTime: modTime.UnixNano(),
	}
	entry := runtime.ApplyUploadModTime(pending, drive.Entry{ID: "uploaded-id", Name: "uploaded.txt", Size: 7})
	if !entry.ModTime.Equal(modTime) {
		t.Fatalf("entry modtime = %s, want %s", entry.ModTime, modTime)
	}
	if got := fs.localModTimeFor(pending.Path); !got.Equal(modTime) {
		t.Fatalf("local modtime = %s, want %s", got, modTime)
	}

	fs.view.mu.Lock()
	fs.view.lists["/"] = listCacheEntry{entries: []drive.Entry{{ID: "old", Name: "uploaded.txt"}}, expires: time.Now().Add(time.Hour)}
	fs.view.mu.Unlock()
	runtime.CommitUploadedEntry(pending.Path, entry)
	fs.view.mu.RLock()
	committed, ok := fs.view.entries.Get(pending.Path)
	_, listCached := fs.view.lists["/"]
	fs.view.mu.RUnlock()
	if !ok || committed.ID != entry.ID {
		t.Fatalf("committed entry = %+v, ok=%v", committed, ok)
	}
	if listCached {
		t.Fatal("parent list cache was not invalidated")
	}
}

func TestVFSUploadRuntimeSeedsReadCache(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), CacheMaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	runtime := newVFSUploadRuntime(fs)
	localPath := filepath.Join(t.TempDir(), "upload.staging")
	if err := os.WriteFile(localPath, []byte("cached upload"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime.SeedReadCache(drive.Entry{ID: "cached-id", Size: int64(len("cached upload")), ModTime: time.Now()}, localPath)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Bytes == 0 || cache.ChunkCount == 0 {
		t.Fatalf("runtime did not seed read cache: %+v", cache)
	}
}
