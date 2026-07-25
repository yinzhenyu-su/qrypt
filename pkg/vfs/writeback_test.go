package vfs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestSeedReadCacheFromStagingSkipsLargeFiles(t *testing.T) {
	remote := t.TempDir()
	raw := localfs.New(remote)
	fs, err := New(raw, Options{CacheDir: t.TempDir(), CacheMaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	smallPath := filepath.Join(t.TempDir(), "small.staging")
	if err := os.WriteFile(smallPath, []byte("small upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	modTime := time.Now()
	fs.seedReadCacheFromStaging(drive.Entry{ID: "small-id", Size: int64(len("small upload")), ModTime: modTime}, smallPath)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	smallCache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if smallCache.Bytes == 0 || smallCache.ChunkCount == 0 {
		t.Fatalf("small staging file was not seeded into read cache: %+v", smallCache)
	}

	largePath := filepath.Join(t.TempDir(), "large.staging")
	if err := os.WriteFile(largePath, bytes.Repeat([]byte("x"), 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	fs.seedReadCacheFromStaging(drive.Entry{ID: "large-id", Size: readCacheLargeFileBytes, ModTime: modTime}, largePath)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	largeCache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if largeCache.Bytes != smallCache.Bytes || largeCache.ChunkCount != smallCache.ChunkCount {
		t.Fatalf("large staging file changed read cache: before=%+v after=%+v", smallCache, largeCache)
	}
}
