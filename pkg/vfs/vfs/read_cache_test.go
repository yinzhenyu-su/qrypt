package vfs_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

func TestVFSDebugReadCacheCountsHitsAndMisses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := []byte("cache me")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	rc, err = fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Misses == 0 || cache.Hits == 0 || cache.Puts == 0 || cache.ChunkCount == 0 {
		t.Fatalf("expected cache hit/miss/put stats, got %+v", cache)
	}
	if len(cache.Files) == 0 {
		t.Fatalf("expected per-file cache details, got %+v", cache)
	}
}

func TestVFSDebugReadCacheReportsPendingJournalDuplicates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cacheDir := t.TempDir()
	fs, err := vfs.New(drive.NewFakeDriver(), vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	if err := fs.Create(ctx, "/qrypt.log"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/qrypt.log", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	pending := fs.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	journalPath := filepath.Join(cacheDir, "pending.jsonl")
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1100; i++ {
		line := fmt.Sprintf(
			`{"op":"dirty","path":"/qrypt.log","fid":"qrypt.log","parent_id":"root","name":"qrypt.log","local_path":%q,"size":4,"updated_at":%d}`+"\n",
			pending[0].LocalPath,
			i+1,
		)
		if _, err := f.WriteString(line); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	journal := fs.DebugSnapshot().Mounts[0].ReadCacheState().Journal
	if journal == nil {
		t.Fatal("journal debug state is nil")
	}
	if !journal.Exists || journal.Entries < 1100 || journal.DuplicateEntries < 1000 || !journal.CompactRecommended {
		t.Fatalf("unexpected journal summary: %+v", journal)
	}
	if len(journal.LargestPaths) == 0 || journal.LargestPaths[0].Path != "/qrypt.log" {
		t.Fatalf("unexpected largest paths: %+v", journal.LargestPaths)
	}
	top := journal.LargestPaths[0]
	if !top.StagingExists || !top.SizeMatches || top.StagingSize != 4 {
		t.Fatalf("unexpected top journal path staging summary: %+v", top)
	}
}

func TestVFSReadCachePersistsAcrossRemount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := []byte("cache me after remount")
	cacheDir := t.TempDir()
	drv := newCountingReadDriver(data)

	fs1, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs1.CloseReadCache() })
	rc, err := fs1.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("first read = %q, want %q", got, data)
	}
	if count := drv.readCount(0); count != 1 {
		t.Fatalf("driver read count after first read = %d, want 1", count)
	}
	if err := fs1.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	fs2, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs2.CloseReadCache() })
	rc, err = fs2.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("second read = %q, want %q", got, data)
	}
	if count := drv.readCount(0); count != 1 {
		t.Fatalf("driver read count after remount = %d, want cached read without driver call", count)
	}
}

func TestVFSReadCacheHandlesSlashIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := []byte("cache me")
	drv := newCountingReadDriver(data)
	drv.id = "/未命名文件夹/运维必读.txt"
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	cache := fs.DebugSnapshot().Mounts[0].ReadCacheState()
	if cache.Puts != 1 || cache.ChunkCount != 1 {
		t.Fatalf("expected one cached chunk for slash ID, got %+v", cache)
	}
	if len(cache.Files) != 1 || strings.Contains(cache.Files[0].ID, "/") || len(cache.Files[0].ID) != sha256.Size*2 {
		t.Fatalf("expected safe hashed ID in debug cache details, got %+v", cache.Files)
	}
}

func TestVFSOverwriteInvalidatesReadCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{"index.html": "old content"}); err != nil {
		t.Fatal(err)
	}
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })
	defer stopVFS(t, fs)
	fs.Start(ctx)

	rc, err := fs.Read(ctx, "/index.html", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(oldData) != "old content" {
		t.Fatalf("old read = %q", oldData)
	}
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	if cache := fs.DebugSnapshot().Mounts[0].ReadCacheState(); cache.ChunkCount == 0 {
		t.Fatalf("expected old content to be cached, got %+v", cache)
	}

	if err := fs.Truncate(ctx, "/index.html", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/index.html", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/index.html"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rc, err = fs.Read(ctx, "/index.html", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(newData) != "new" {
		t.Fatalf("read after overwrite = %q, want new", newData)
	}
}
