package vfs

import (
	"bytes"
	"context"
	"io"
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
	fs, err := New(raw, Options{StorageDir: t.TempDir(), CacheMaxBytes: 64 << 20})
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

func TestUploadSourceSeedsReadCache(t *testing.T) {
	remote := t.TempDir()
	fs, err := New(localfs.New(remote), Options{StorageDir: t.TempDir(), CacheMaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	var invalidated []string
	unsubscribe := fs.SubscribeInvalidations(func(path string) {
		invalidated = append(invalidated, path)
	})
	t.Cleanup(unsubscribe)

	content := []byte("direct upload cache seed")
	entry, err := fs.UploadSource(context.Background(), "/direct.txt", SourceUploadRequest{
		Source: drive.NewBytesReadOnlyFileSource(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Bytes == 0 || cache.ChunkCount == 0 {
		t.Fatalf("direct upload was not seeded into read cache: %+v", cache)
	}

	if err := os.WriteFile(filepath.Join(remote, "direct.txt"), []byte("remote changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Read(context.Background(), "/direct.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("read after direct upload = %q, want cached %q", got, content)
	}
	if entry.ID == "" {
		t.Fatal("uploaded entry has empty id")
	}
	if len(invalidated) != 1 || invalidated[0] != "/direct.txt" {
		t.Fatalf("invalidations = %v, want exactly one /direct.txt", invalidated)
	}
}

func TestSeedReadCacheFromSourceSkipsLargeFiles(t *testing.T) {
	fs, err := New(localfs.New(t.TempDir()), Options{StorageDir: t.TempDir(), CacheMaxBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	source := panicOpenSource{size: readCacheLargeFileBytes}
	fs.seedReadCacheFromSource(context.Background(), drive.Entry{ID: "large-direct", Size: source.Size(), ModTime: time.Now()}, source)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Bytes != 0 || cache.ChunkCount != 0 {
		t.Fatalf("large direct source changed read cache: %+v", cache)
	}
}

type panicOpenSource struct {
	size int64
}

func (s panicOpenSource) Size() int64 {
	return s.size
}

func (panicOpenSource) Open(context.Context) (drive.ReadOnlyFile, error) {
	panic("large source should not be opened")
}
