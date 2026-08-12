package vfs

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// BenchmarkVFSStatCached measures the full Stat path (pendingUpload + resolve
// cache hit + local mtime + health record + debug active tracking) for a
// cached entry — the shape of a FUSE getattr.
func BenchmarkVFSStatCached(b *testing.B) {
	fs := benchVFS(b, 1)
	if _, err := fs.Stat(context.Background(), "/file-0.txt"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Stat(context.Background(), "/file-0.txt"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDebugActiveTracking isolates the unconditional per-op debug cost:
// begin (fmt.Sprintf + mutex + map write + clone) + finish (mutex + delete).
func BenchmarkDebugActiveTracking(b *testing.B) {
	fs := benchVFS(b, 1)
	op := debugActiveOp{Kind: "vfs_read", Phase: "resolve", Path: "/file-0.txt", Offset: 0, Requested: 4096}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fs.beginDebugActive(op)
		fs.updateDebugActive(id, func(o *debugActiveOp) { o.Phase = "read_range" })
		fs.finishDebugActive(id)
	}
}

func benchVFS(b *testing.B, files int) *VFS {
	b.Helper()
	fs, err := New(localfs.New(b.TempDir()), Options{StorageDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < files; i++ {
		path := "/file-" + itoa(i) + ".txt"
		if err := fs.Create(ctx, path); err != nil {
			b.Fatal(err)
		}
	}
	return fs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
