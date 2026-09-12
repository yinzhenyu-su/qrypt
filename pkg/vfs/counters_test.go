package vfs_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestVFSDebugSnapshotCarriesReadCounters: the per-mount snapshot exposes the
// cumulative read counters alongside the bounded event history.
func TestVFSDebugSnapshotCarriesReadCounters(t *testing.T) {
	ctx := context.Background()
	data := bytes.Repeat([]byte("c"), 2*testReadChunkSize)
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
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()

	counters := fs.DebugSnapshot().Mounts[0].Runtime.Counters
	if counters.Ops != 1 {
		t.Fatalf("counter ops = %d, want 1", counters.Ops)
	}
	if counters.Errors != 0 {
		t.Fatalf("counter errors = %d, want 0", counters.Errors)
	}
	if counters.Bytes != uint64(len(got)) {
		t.Fatalf("counter bytes = %d, want %d", counters.Bytes, len(got))
	}
	if got := counters.Latency.Samples(); got != 1 {
		t.Fatalf("latency samples = %d, want 1", got)
	}
	if len(counters.Latency.BoundsMS) == 0 || len(counters.Latency.Counts) != len(counters.Latency.BoundsMS)+1 {
		t.Fatalf("histogram shape = %d bounds / %d counts", len(counters.Latency.BoundsMS), len(counters.Latency.Counts))
	}
	if _, _, ok := counters.QuantileMS(0.95); !ok {
		t.Fatal("quantile reported no samples after a successful read")
	}
}

// TestVFSDebugSnapshotCountersOnIdleMount: a mount that never served a read
// still reports a well-formed zero counter set.
func TestVFSDebugSnapshotCountersOnIdleMount(t *testing.T) {
	drv := newCountingReadDriver([]byte("payload"))
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	counters := fs.DebugSnapshot().Mounts[0].Runtime.Counters
	if counters.Ops != 0 || counters.Errors != 0 || counters.Bytes != 0 || counters.MeanMS != 0 {
		t.Fatalf("idle mount counters = %+v", counters)
	}
	if len(counters.Latency.Counts) == 0 {
		t.Fatal("idle mount histogram counts are missing")
	}
	if _, _, ok := counters.QuantileMS(0.95); ok {
		t.Fatal("idle mount reported a quantile")
	}
}

// TestVFSDebugSnapshotCountersSeparateFailures: failed reads count toward ops
// and errors without contributing bytes.
func TestVFSDebugSnapshotCountersSeparateFailures(t *testing.T) {
	ctx := context.Background()
	drv := newCountingReadDriver([]byte("payload"))
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.CloseReadCache() })

	if _, err := fs.Read(ctx, "/missing.bin", 0, 4); err == nil {
		t.Fatal("want resolve error for a missing path")
	}
	rc, err := fs.Read(ctx, "/data.bin", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()

	counters := fs.DebugSnapshot().Mounts[0].Runtime.Counters
	if counters.Ops != 2 {
		t.Fatalf("counter ops = %d, want 2", counters.Ops)
	}
	if counters.Errors != 1 {
		t.Fatalf("counter errors = %d, want 1", counters.Errors)
	}
	if counters.Bytes != 4 {
		t.Fatalf("counter bytes = %d, want 4 (the failed read's bytes must not count)", counters.Bytes)
	}
}
