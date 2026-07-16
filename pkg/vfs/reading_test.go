package vfs_test

import (
	"bytes"
	"context"
	"io"
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

func TestVFSReadPrefetchesAdjacentChunk(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("b"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	waitForCondition(t, func() bool {
		return drv.readCount(testReadChunkSize) == 1
	})
	before := drv.readCount(testReadChunkSize)

	rc, err = fs.Read(ctx, "/data.bin", testReadChunkSize, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if got := drv.readCount(testReadChunkSize); got != before {
		t.Fatalf("prefetched chunk read count = %d, want %d", got, before)
	}
}

func TestVFSReadWaitsForInFlightPrefetch(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("c"), 3*testReadChunkSize)
	drv := newCountingReadDriver(data)
	entered, release := drv.blockRead(testReadChunkSize)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	rc, err := fs.Read(ctx, "/data.bin", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("prefetch did not start")
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
	if got := drv.readCount(testReadChunkSize); got != 1 {
		t.Fatalf("in-flight chunk read count = %d, want 1", got)
	}
	release()
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if got := drv.readCount(testReadChunkSize); got != 1 {
		t.Fatalf("completed chunk read count = %d, want 1", got)
	}
}
