package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVFSZeroByteFlushWaitsForFollowUpWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if got := drv.uploadCount(); got != 0 {
		t.Fatalf("zero-byte flush uploaded too early: count=%d", got)
	}

	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("final"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if got := drv.lastUpload(); got != "final" {
		t.Fatalf("last upload = %q, want final", got)
	}
}
func TestVFSAppleMetadataWrittenAndUploaded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if n, err := fs.WriteAt(ctx, "/.DS_Store", []byte("finder"), 0); err != nil || n != len("finder") {
		t.Fatalf("WriteAt .DS_Store n=%d err=%v", n, err)
	}
	if err := fs.Flush(ctx, "/.DS_Store"); err != nil {
		t.Fatal(err)
	}

	// After flush, the file is pending and will be uploaded (like any normal file).
	pending := fs.PendingUploads()
	if len(pending) != 1 || pending[0].Name != ".DS_Store" {
		t.Fatalf("pending = %v, want [.DS_Store]", pending)
	}

	// Stat finds the pending file.
	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 6 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}
func TestVFSRemoteAppleMetadataVisible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{entries: map[string]drive.Entry{
		"meta":   {ID: "meta", ParentID: "0", Name: ".DS_Store", Size: 1},
		"double": {ID: "double", ParentID: "0", Name: "._asset.js", Size: 1},
		"file":   {ID: "file", ParentID: "0", Name: "asset.js", Size: 1},
	}}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}

	// Apple metadata files are now visible like any other file.
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	got := namesOf(entries)
	if !strings.Contains(got, ".DS_Store") || !strings.Contains(got, "._asset.js") || !strings.Contains(got, "asset.js") {
		t.Fatalf("entries = %q, want all three entries including .DS_Store and ._asset.js", got)
	}

	info, err := fs.Stat(ctx, "/.DS_Store")
	if err != nil {
		t.Fatalf("Stat .DS_Store err=%v", err)
	}
	if info.Name != ".DS_Store" || info.Size != 1 {
		t.Fatalf("Stat .DS_Store = %+v", info)
	}
}
func TestVFSWriteAtStagesExistingFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "data.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{RootID: remote, StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("XY"), 2); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	data, err := os.ReadFile(filepath.Join(remote, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abXYef" {
		t.Fatalf("unexpected patched backend data: %q", data)
	}
}
func TestVFSTruncateUploadedFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/data.txt", []byte("abcdef"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Truncate(ctx, "/data.txt", 3); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Read(ctx, "/data.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected staged truncate data: %q", data)
	}
	if err := fs.Flush(ctx, "/data.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err = os.ReadFile(remote + "/data.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("unexpected truncated backend data: %q", data)
	}
}
