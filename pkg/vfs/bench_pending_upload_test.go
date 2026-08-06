package vfs

import (
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// Every FUSE Getattr/Read on a path with a pending upload goes through
// pendingUpload. It used to scan all pending uploads (and stat every staging
// file); now it is a single map lookup. This benchmark quantifies the
// per-operation cost as the number of pending uploads grows.

func benchVFSWithPendingUploads(b *testing.B, pending int) *VFS {
	fs, err := New(localfs.New(b.TempDir()), Options{StorageDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < pending; i++ {
		path := fmt.Sprintf("/pending-%d.txt", i)
		if err := fs.upload.store.SaveUpload(PendingUpload{Path: path, FID: fmt.Sprintf("fid-%d", i), Name: fmt.Sprintf("pending-%d.txt", i), LocalPath: path}); err != nil {
			b.Fatal(err)
		}
	}
	return fs
}

func BenchmarkPendingUpload1(b *testing.B)    { benchPendingUpload(b, 1) }
func BenchmarkPendingUpload100(b *testing.B)  { benchPendingUpload(b, 100) }
func BenchmarkPendingUpload1000(b *testing.B) { benchPendingUpload(b, 1000) }

func benchPendingUpload(b *testing.B, pending int) {
	fs := benchVFSWithPendingUploads(b, pending)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.pendingUpload("/pending-0.txt"); err != nil {
			b.Fatal(err)
		}
	}
}
