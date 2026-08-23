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
	vfsread "github.com/yinzhenyu/qrypt/pkg/vfs/read"
)

func TestVFSReadSpansChunks(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("a"), testReadChunkSize+10)
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
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read length = %d, want %d", len(got), len(data))
	}
	reads := fs.DebugSnapshot().Mounts[0].ReadEvents()
	var summaryCount int
	var sawDetail bool
	for _, read := range reads {
		if read.Phase == "read" {
			summaryCount++
			if read.Chunks != 2 {
				t.Fatalf("summary read chunks = %+v, want 2", read)
			}
		}
		if read.ParentOpID != "" {
			sawDetail = true
		}
	}
	if summaryCount != 1 || !sawDetail {
		t.Fatalf("read events = %+v, want one summary and chunk details", reads)
	}
}

func TestVFSReadPastEOFReturnsEmptyWithoutDriverRead(t *testing.T) {
	ctx := context.Background()
	data := []byte("small")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

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

func TestVFSReadAllLargeBinaryPreservesEveryChunk(t *testing.T) {
	ctx := context.Background()
	data := make([]byte, 8<<20)
	for i := range data {
		data[i] = byte(i*31 + i/int(vfsread.ChunkSize))
	}
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		for offset := 0; offset < len(data); offset += int(vfsread.ChunkSize) {
			end := min(offset+int(vfsread.ChunkSize), len(data))
			if !bytes.Equal(got[offset:end], data[offset:end]) {
				t.Fatalf("chunk mismatch at offset %d", offset)
			}
		}
		t.Fatalf("large read length = %d, want %d", len(got), len(data))
	}
}

func TestVFSReadClampsDriverReadToEntrySize(t *testing.T) {
	ctx := context.Background()
	data := []byte("small")
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

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
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

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

}

func TestVFSReadExactChunkMissLoadsSingleChunk(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("w"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

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
}

func TestVFSReadDebugIncludesWindowAndDriverTiming(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("t"), 16*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	rc, err = fs.Read(ctx, "/data.bin", int64(8*testReadChunkSize), 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	events := fs.DebugSnapshot().Mounts[0].ReadEvents()
	// A small history ring is valid; it may retain too few events to include
	// both the window detail and its timing fields after multiple reads.
	if vfsread.HistoryLimit < 4 {
		return
	}
	var sawWindow, sawDriverTiming bool
	for _, event := range events {
		if _, ok := event.Extra["prefetch_chunks"]; ok {
			t.Fatalf("read debug event still includes removed prefetch_chunks field: %+v", event)
		}
		switch event.Phase {
		case "cache_miss_load":
			if event.Extra["window_chunks"] != nil {
				sawWindow = true
			}
		case "fetch_window":
			_, hasOpen := event.Extra["driver_open_ms"]
			_, hasBody := event.Extra["driver_body_ms"]
			_, hasClose := event.Extra["driver_close_ms"]
			if hasOpen && hasBody && hasClose {
				sawDriverTiming = true
			}
		case "fetch_chunk", "wait_chunk_load":
			t.Fatalf("read debug event still uses removed chunk load Phase: %+v", event)
		}
	}
	if !sawWindow {
		t.Fatal("read debug events missing read window field")
	}
	if !sawDriverTiming {
		t.Fatal("read debug events missing driver timing fields")
	}
}

func TestVFSReadWaitsForInFlightPrefetchWindow(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("c"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	entered, release := drv.blockRead(testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("prefetch window did not start")
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
		return drv.readCount(testReadChunkSize) == 1
	})
	prefetchReads := drv.readCount(testReadChunkSize)
	waitForCondition(t, func() bool {
		return drv.readCount(2*testReadChunkSize) == 1
	})
	if got := drv.readCount(2 * testReadChunkSize); got != 1 {
		t.Fatalf("lookahead chunk read count = %d, want 1", got)
	}
	release()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if got := drv.readCount(testReadChunkSize); got != prefetchReads {
		t.Fatalf("completed chunk read count = %d, want %d because foreground waited for prefetch", got, prefetchReads)
	}
}

func TestVFSReadStartsAdjacentPrefetchBeforeForegroundMissCompletes(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("p"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	foregroundEntered, releaseForeground := drv.blockRead(0)
	prefetchEntered, releasePrefetch := drv.blockRead(testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	readDone := make(chan error, 1)
	go func() {
		rc, err := fs.Read(ctx, "/data.bin", testReadChunkSize/2, 16)
		if err == nil {
			_ = rc.Close()
		}
		readDone <- err
	}()
	select {
	case <-foregroundEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground window did not start")
	}
	select {
	case <-prefetchEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("adjacent prefetch did not overlap foreground miss")
	}
	releaseForeground()
	releasePrefetch()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
}

func TestVFSActiveOpsExposeBlockedPrefetchAndWaiter(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("x"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	entered, release := drv.blockRead(testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("prefetch window did not start")
	}
	waitForCondition(t, func() bool {
		return activeOpsContain(t, fs, "vfs_prefetch", "fetch_window")
	})

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
		return activeOpsContain(t, fs, "vfs_wait", "wait_window")
	})

	release()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		mounts, err := fs.DebugActiveOps(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(mounts) == 1 && len(mounts[0].Ops) == 0
	})
}

func activeOpsContain(t *testing.T, fs *vfs.VFS, kind, phase string) bool {
	t.Helper()
	mounts, err := fs.DebugActiveOps(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range mounts {
		for _, op := range mount.Ops {
			if op.Kind == kind && op.Phase == phase {
				return true
			}
		}
	}
	return false
}

func TestVFSReadUsesHotChunkBeforeAsyncCacheWriteCompletes(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("d"), testReadChunkSize)
	drv := newCountingReadDriver(data)
	cacheDir := t.TempDir()
	fs, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(cacheDir, "reading"), 0o755)
		_ = fs.CloseReadCache()
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
	fs1, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs1.CloseReadCache() })

	rc, err := fs1.Read(ctx, "/data.bin", testReadChunkSize+32, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	before := drv.readCount(testReadChunkSize)
	if before != 1 {
		t.Fatalf("initial driver read count = %d, want 1", before)
	}
	if err := fs1.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	fs2, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs2.CloseReadCache() })
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
	if got := drv.readCount(testReadChunkSize); got != before {
		t.Fatalf("remounted cached range driver read count = %d, want %d", got, before)
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
	fs1, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs1.CloseReadCache() })

	rc, err := fs1.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if err := fs1.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	fs2, err := vfs.New(drv, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs2.CloseReadCache() })
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
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

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

func TestVFSReadPrefetchesAdjacentChunksConcurrently(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("e"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	firstEntered, releaseFirst := drv.blockRead(testReadChunkSize)
	secondEntered, releaseSecond := drv.blockRead(2 * testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first prefetch did not start")
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second prefetch did not start concurrently")
	}
	releaseFirst()
	releaseSecond()

	waitForCondition(t, func() bool {
		var prefetches int
		for _, event := range fs.DebugSnapshot().Mounts[0].ReadEvents() {
			if event.Phase == "prefetch_window" {
				prefetches++
			}
		}
		return prefetches == 2
	})
}

func TestVFSSequentialReadMergesPrefetchRanges(t *testing.T) {
	ctx := context.Background()
	firstPrefetchChunk := int64(2)
	secondPrefetchChunk := firstPrefetchChunk + int64(vfsread.SequentialPrefetchChunks)
	data := bytes.Repeat([]byte("s"), int((secondPrefetchChunk+int64(vfsread.SequentialPrefetchChunks))*testReadChunkSize))
	drv := newCountingReadDriver(data)
	firstEntered, releaseFirst := drv.blockRead(firstPrefetchChunk * testReadChunkSize)
	secondEntered, releaseSecond := drv.blockRead(secondPrefetchChunk * testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(vfs.WithoutReadPrefetch(ctx), "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	rc, err = fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first merged prefetch did not start")
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second merged prefetch did not start concurrently")
	}
	prefetchSize := int64(vfsread.SequentialPrefetchChunks) * testReadChunkSize
	if got := drv.readSize(firstPrefetchChunk * testReadChunkSize); got != prefetchSize {
		t.Fatalf("first sequential prefetch size = %d, want %d", got, prefetchSize)
	}
	if got := drv.readSize(secondPrefetchChunk * testReadChunkSize); got != prefetchSize {
		t.Fatalf("second sequential prefetch size = %d, want %d", got, prefetchSize)
	}
	releaseFirst()
	releaseSecond()
	waitForCondition(t, func() bool {
		var prefetches int
		for _, event := range fs.DebugSnapshot().Mounts[0].ReadEvents() {
			if event.Phase == "prefetch_window" {
				prefetches++
			}
		}
		return prefetches == 2
	})
}

func TestVFSHandleSequentialReadUsesBoundedMergedPrefetch(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("a"), int((2+int64(vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks))*testReadChunkSize))
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	first := vfsread.WithAccessHint(vfs.WithoutReadPrefetch(ctx), vfsread.AccessHint{SessionID: 1, RequestID: 1})
	rc, err := fs.Read(first, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	second := vfsread.WithAccessHint(ctx, vfsread.AccessHint{SessionID: 1, RequestID: 2})
	rc, err = fs.Read(second, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	waitForCondition(t, func() bool {
		for index := int64(2); index < 2+int64(vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks); index += vfsread.SequentialPrefetchChunks {
			if drv.readCount(index*testReadChunkSize) != 1 {
				return false
			}
		}
		return true
	})
	for index := int64(2); index < 2+int64(vfsread.PrefetchLimit*vfsread.SequentialPrefetchChunks); index += vfsread.SequentialPrefetchChunks {
		if got := drv.readSize(index * testReadChunkSize); got != vfsread.SequentialPrefetchChunks*testReadChunkSize {
			t.Fatalf("adaptive prefetch at chunk %d = %d, want %d", index, got, vfsread.SequentialPrefetchChunks*testReadChunkSize)
		}
	}
}

func TestVFSSequentialReadPreservesWindowAfterCachedHead(t *testing.T) {
	ctx := context.Background()
	firstPrefetchChunk := int64(3)
	secondPrefetchChunk := firstPrefetchChunk + int64(vfsread.SequentialPrefetchChunks)
	data := bytes.Repeat([]byte("h"), int((secondPrefetchChunk+int64(vfsread.SequentialPrefetchChunks))*testReadChunkSize))
	drv := newCountingReadDriver(data)
	firstEntered, releaseFirst := drv.blockRead(firstPrefetchChunk * testReadChunkSize)
	secondEntered, releaseSecond := drv.blockRead(secondPrefetchChunk * testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	for _, offset := range []int64{2 * testReadChunkSize, 0} {
		rc, err := fs.Read(vfs.WithoutReadPrefetch(ctx), "/data.bin", offset, testReadChunkSize)
		if err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
	}
	rc, err := fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-firstEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("shifted merged prefetch did not start")
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second shifted prefetch did not start concurrently")
	}
	prefetchSize := int64(vfsread.SequentialPrefetchChunks) * testReadChunkSize
	if got := drv.readSize(firstPrefetchChunk * testReadChunkSize); got != prefetchSize {
		t.Fatalf("shifted sequential prefetch size = %d, want %d", got, prefetchSize)
	}
	if got := drv.readSize(secondPrefetchChunk * testReadChunkSize); got != prefetchSize {
		t.Fatalf("second shifted prefetch size = %d, want %d", got, prefetchSize)
	}
	releaseFirst()
	releaseSecond()
	waitForCondition(t, func() bool {
		var prefetches int
		for _, event := range fs.DebugSnapshot().Mounts[0].ReadEvents() {
			if event.Phase == "prefetch_window" {
				prefetches++
			}
		}
		return prefetches == 2
	})
}

func TestVFSOffsetJumpKeepsSingleChunkPrefetch(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("j"), 6*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(vfs.WithoutReadPrefetch(ctx), "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	rc, err = fs.Read(ctx, "/data.bin", 2*testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	waitForCondition(t, func() bool {
		return drv.readCount(3*testReadChunkSize) == 1 && drv.readCount(4*testReadChunkSize) == 1
	})
	if got := drv.readSize(3 * testReadChunkSize); got != testReadChunkSize {
		t.Fatalf("first jump prefetch size = %d, want %d", got, testReadChunkSize)
	}
	if got := drv.readSize(4 * testReadChunkSize); got != testReadChunkSize {
		t.Fatalf("second jump prefetch size = %d, want %d", got, testReadChunkSize)
	}
}

func TestVFSHandleOffsetJumpSkipsSpeculativePrefetch(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("j"), 6*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	first := vfsread.WithAccessHint(vfs.WithoutReadPrefetch(ctx), vfsread.AccessHint{SessionID: 1, RequestID: 1})
	rc, err := fs.Read(first, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	second := vfsread.WithAccessHint(ctx, vfsread.AccessHint{SessionID: 1, RequestID: 2})
	rc, err = fs.Read(second, "/data.bin", 2*testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if got := drv.readCount(2 * testReadChunkSize); got != 1 {
		t.Fatalf("jump foreground read count = %d, want 1", got)
	}
	if got := drv.readCount(3*testReadChunkSize) + drv.readCount(4*testReadChunkSize); got != 0 {
		t.Fatalf("jump speculative reads = %d, want 0", got)
	}
}

func TestVFSHandleUnalignedSeekLoadsTouchedChunksTogether(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("u"), 4*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	ctx = vfsread.WithAccessHint(ctx, vfsread.AccessHint{SessionID: 1, RequestID: 1})
	rc, err := fs.Read(ctx, "/data.bin", testReadChunkSize/2, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	if got := drv.readCount(0); got != 1 {
		t.Fatalf("unaligned seek foreground read count = %d, want 1", got)
	}
	if got := drv.readSize(0); got != 2*testReadChunkSize {
		t.Fatalf("unaligned seek range size = %d, want %d", got, 2*testReadChunkSize)
	}
}

func TestVFSReadWithoutPrefetchSkipsAdjacentChunk(t *testing.T) {
	ctx := vfs.WithoutReadPrefetch(context.Background())
	data := bytes.Repeat([]byte("e"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	rc, err := fs.Read(ctx, "/data.bin", 0, testReadChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	time.Sleep(50 * time.Millisecond)

	if got := drv.readCount(testReadChunkSize); got != 0 {
		t.Fatalf("prefetch read count = %d, want 0", got)
	}
}
