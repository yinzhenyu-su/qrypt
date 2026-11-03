package vfs_test

import (
	"bytes"
	"context"
	"io"
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
