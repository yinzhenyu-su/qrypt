package vfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestVFSUploadRuntimeSeedsReadCache(t *testing.T) {
	t.Parallel()
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), CacheMaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	localPath := filepath.Join(t.TempDir(), "upload.staging")
	if err := os.WriteFile(localPath, []byte("cached upload"), 0o644); err != nil {
		t.Fatal(err)
	}

	newVFSViewCommitter(fs).CommitUploadedEntry("/f.txt", drive.Entry{ID: "cached-id", Size: int64(len("cached upload")), ModTime: time.Now()}, localPath)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Bytes == 0 || cache.ChunkCount == 0 {
		t.Fatalf("runtime did not seed read cache: %+v", cache)
	}
}
