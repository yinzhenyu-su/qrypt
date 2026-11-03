package vfs_test

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type parallelCountingDriver struct {
	*countingReadDriver
}

func (d *parallelCountingDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityParallelRange}
}

type strictFallbackDriver struct {
	*countingReadDriver
	fullReads   atomic.Int32
	strictCalls atomic.Int32
}

func (d *strictFallbackDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilityParallelRange}
}

func (d *strictFallbackDriver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if drive.StrictRangeFromContext(ctx) {
		d.strictCalls.Add(1)
		return nil, drive.ErrUnsupported
	}
	d.fullReads.Add(1)
	return d.countingReadDriver.Read(ctx, entry, offset, size)
}

func TestVFSBulkReadSkipsDurableCache(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 16<<20)
	driver := &parallelCountingDriver{countingReadDriver: newCountingReadDriver(data)}
	fs, err := vfs.New(driver, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	ctx := vfs.WithReadIntent(context.Background(), vfs.ReadIntentBulk)
	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("bulk read returned different bytes")
	}
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	snapshot := fs.DebugSnapshot()
	if len(snapshot.Mounts) != 1 {
		t.Fatalf("mounts = %d, want 1", len(snapshot.Mounts))
	}
	if got := snapshot.Mounts[0].Cache.ChunkCount; got != 0 {
		t.Fatalf("durable cache chunk count = %d, want 0 for a bulk read", got)
	}
}

func TestVFSBulkReadFallsBackWhenStrictRangeUnsupported(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 16<<20)
	driver := &strictFallbackDriver{countingReadDriver: newCountingReadDriver(data)}
	fs, err := vfs.New(driver, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	ctx := vfs.WithReadIntent(context.Background(), vfs.ReadIntentBulk)
	rc, err := fs.Read(ctx, "/data.bin", 0, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fallback read returned different bytes")
	}
	if got := driver.fullReads.Load(); got > 4 {
		t.Fatalf("full-stream reads = %d (strict calls=%d), want at most one fallback per window", got, driver.strictCalls.Load())
	}
	if driver.strictCalls.Load() == 0 {
		t.Fatal("parallel sub-ranges did not use strict range reads")
	}
}
