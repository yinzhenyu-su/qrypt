package vfs_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// Download-style read benchmarks. downloadOne currently reads the whole file
// in one VFS.Read call (whole-file materialization in readRange's bytes.Buffer)
// and then copies it to disk. Windowed reads go through the same chunk path
// but bound memory to the window size.
//
// Both benchmarks pre-warm the read cache with one full pass so they measure
// the steady-state VFS.Read data path (chunk slicing, materialization,
// instrumentation) rather than cold driver fetch.

const benchReadSize = 32 << 20 // 32 MiB

func benchReadFS(b *testing.B) *vfs.VFS {
	data := bytes.Repeat([]byte("a"), benchReadSize)
	drv := newCountingReadDriver(data)
	fs, err := vfs.New(drv, vfs.Options{StorageDir: b.TempDir(), CacheMaxBytes: 256 << 20})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = fs.CloseReadCache() })
	return fs
}

func warmReadCache(b *testing.B, fs *vfs.VFS) {
	ctx := context.Background()
	rc, err := fs.Read(ctx, "/data.bin", 0, benchReadSize)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		b.Fatal(err)
	}
	rc.Close()
}

// One full-file Read, then drain the returned reader (downloadOne pattern).
func BenchmarkVFSReadWholeFile(b *testing.B) {
	fs := benchReadFS(b)
	warmReadCache(b, fs)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rc, err := fs.Read(ctx, "/data.bin", 0, benchReadSize)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			b.Fatal(err)
		}
		rc.Close()
	}
}

// Same bytes, read in 1 MiB windowed calls (memory bounded per window).
func BenchmarkVFSReadWindowed(b *testing.B) {
	fs := benchReadFS(b)
	warmReadCache(b, fs)
	ctx := context.Background()
	const window = 1 << 20
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var off int64
		for off < benchReadSize {
			rc, err := fs.Read(ctx, "/data.bin", off, window)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, rc); err != nil {
				b.Fatal(err)
			}
			rc.Close()
			off += window
		}
	}
}
