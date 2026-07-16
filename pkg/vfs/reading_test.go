package vfs_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSReadSpansChunks(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("a"), testReadChunkSize+10)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read length = %d, want %d", len(got), len(data))
	}
	reads := fs.DebugSnapshot().Mounts[0].ReadEvents()
	if len(reads) != 1 || reads[0].Chunks != 2 {
		t.Fatalf("read chunks = %+v, want one read spanning 2 chunks", reads)
	}
}

func TestVFSReadPastEOFReturnsEmptyWithoutDriverRead(t *testing.T) {
	ctx := context.Background()
	data := []byte("small")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 4096, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("read past EOF returned %q, want empty", got)
	}
	if got := drv.readCount(4096); got != 0 {
		t.Fatalf("driver read count at EOF offset = %d, want 0", got)
	}
	reads := fs.DebugSnapshot().Mounts[0].ReadEvents()
	if len(reads) != 1 || reads[0].Chunks != 0 {
		t.Fatalf("read chunks = %+v, want one empty read with 0 chunks", reads)
	}
}

func TestVFSReadClampsDriverReadToEntrySize(t *testing.T) {
	ctx := context.Background()
	data := []byte("small")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "small" {
		t.Fatalf("read = %q, want small", got)
	}
	if got := drv.readSize(0); got != int64(len(data)) {
		t.Fatalf("driver read size = %d, want %d", got, len(data))
	}
}

func TestVFSReadSmallMissLoadsSingleChunk(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("b"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if got := drv.readCount(0); got != 1 {
		t.Fatalf("foreground chunk read count = %d, want 1", got)
	}
	if got := drv.readSize(0); got != testReadChunkSize {
		t.Fatalf("foreground chunk read size = %d, want %d", got, testReadChunkSize)
	}

	rc, err = fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if got := drv.readCount(testReadChunkSize); got != 1 {
		t.Fatalf("second chunk read count = %d, want 1", got)
	}
}

func TestVFSReadChunkMissLoadsForegroundWindow(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("w"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if got := drv.readCount(0); got != 1 {
		t.Fatalf("foreground window read count = %d, want 1", got)
	}
	if got := drv.readSize(0); got != int64(len(data)) {
		t.Fatalf("foreground window read size = %d, want %d", got, len(data))
	}

	rc, err = fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if got := drv.readCount(testReadChunkSize); got != 0 {
		t.Fatalf("window-covered chunk read count = %d, want 0", got)
	}
}

func TestVFSReadWaitsForInFlightForegroundWindow(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("c"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	entered, release := drv.blockRead(0)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	firstDone := make(chan error, 1)
	go func() {
		rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
		if err != nil {
			firstDone <- err
			return
		}
		_ = rc.Close()
		firstDone <- nil
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground window did not start")
	}

	readDone := make(chan error, 1)
	go func() {
		rc, err := fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
		if err != nil {
			readDone <- err
			return
		}
		_ = rc.Close()
		readDone <- nil
	}()

	waitForCondition(t, func() bool {
		return drv.readCount(0) == 1
	})
	if got := drv.readCount(testReadChunkSize); got != 0 {
		t.Fatalf("in-flight chunk read count = %d, want 0", got)
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if got := drv.readCount(0); got != 1 {
		t.Fatalf("completed foreground window read count = %d, want 1", got)
	}
	if got := drv.readCount(testReadChunkSize); got != 0 {
		t.Fatalf("completed chunk read count = %d, want 0", got)
	}
}

func TestVFSReadUsesHotChunkBeforeAsyncCacheWriteCompletes(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("d"), testReadChunkSize)
	drv := newCountingReadDriver(data)
	cacheDir := t.TempDir()
	fs, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(cacheDir, "reading"), 0o755)
		_ = fs.FlushReadCache()
	})
	if err := os.Chmod(filepath.Join(cacheDir, "reading"), 0o555); err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	rc, err = fs.Read(ctx, "/data.bin", 32, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if got := drv.readCount(0); got != 1 {
		t.Fatalf("driver read count for hot chunk = %d, want 1", got)
	}
}

func TestVFSReadRangeUsesPersistedCacheAfterRemount(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("r"), 2*testReadChunkSize)
	copy(data[testReadChunkSize+32:testReadChunkSize+48], []byte("0123456789abcdef"))
	drv := newCountingReadDriver(data)
	cacheDir := t.TempDir()
	fs1, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs1.FlushReadCache() })

	rc, err := fs1.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if got := drv.readCount(0); got != 1 {
		t.Fatalf("initial driver read count = %d, want 1", got)
	}
	if err := fs1.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	fs2, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs2.FlushReadCache() })
	rc, err = fs2.Read(ctx, "/data.bin", testReadChunkSize+32, 16)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "0123456789abcdef" {
		t.Fatalf("cached range = %q", got)
	}
	if got := drv.readCount(testReadChunkSize); got != 0 {
		t.Fatalf("remounted cached range driver read count = %d, want 0", got)
	}
}

func TestVFSReadPromotesPersistedCacheRangeToHotChunk(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("h"), testReadChunkSize)
	copy(data[32:48], []byte("0123456789abcdef"))
	copy(data[64:80], []byte("fedcba9876543210"))
	copy(data[96:112], []byte("0011223344556677"))
	drv := newCountingReadDriver(data)
	cacheDir := t.TempDir()
	fs1, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs1.FlushReadCache() })

	rc, err := fs1.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if err := fs1.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	fs2, err := vfs.New(drv, vfs.Options{CacheDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs2.FlushReadCache() })
	rc, err = fs2.Read(ctx, "/data.bin", 32, 16)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "0123456789abcdef" {
		t.Fatalf("first cached range = %q", got)
	}
	rc, err = fs2.Read(ctx, "/data.bin", 64, 16)
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(rc)
	closeErr = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "fedcba9876543210" {
		t.Fatalf("second cached range = %q", got)
	}

	matches, err := filepath.Glob(filepath.Join(cacheDir, "reading", "*.batch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	rc, err = fs2.Read(ctx, "/data.bin", 96, 16)
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(rc)
	closeErr = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(got) != "0011223344556677" {
		t.Fatalf("hot cached range = %q", got)
	}
}

func TestVFSReadRejectsDriverOverread(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("o"), testReadChunkSize)
	drv := &overReadDriver{countingReadDriver: newCountingReadDriver(data)}
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err == nil {
		_, readErr := io.ReadAll(rc)
		closeErr := rc.Close()
		if readErr == nil && closeErr == nil {
			t.Fatal("expected overread error")
		}
		return
	}
	if !strings.Contains(err.Error(), "driver returned more data than requested") {
		t.Fatal("expected overread error")
	}
}

func TestVFSReadWindowAvoidsBackPrefetchForCoveredChunks(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("e"), 20*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.FlushReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	rc, err = fs.Read(ctx, "/data.bin", 7*testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	time.Sleep(100 * time.Millisecond)
	if got := drv.readCount(7 * testReadChunkSize); got != 0 {
		t.Fatalf("covered chunk read count = %d, want 0", got)
	}
}
