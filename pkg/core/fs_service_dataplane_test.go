package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestCoreFSServiceDataPlane drives the transport bridge against a real
// per-mount VFS so the CLI data plane keeps the same behavior the mounted and
// mobile surfaces see: entries carry identity, misses carry a hint, and uploads
// drain through the same engine.
func TestCoreFSServiceDataPlane(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		Name:          "loc",
		StorageDir:    filepath.Join(t.TempDir(), "cache"),
		UploadDelay:   10 * time.Millisecond,
		DeleteDelay:   10 * time.Millisecond,
		UploadWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	svc := coreFSService{core: newTestCore(t, fs)}

	if _, err := svc.Mkdir(ctx, "/dir"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entry, err := svc.Stat(ctx, "/dir")
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !entry.IsDir || entry.Path != "/dir" {
		t.Fatalf("stat dir = %+v, want directory at /dir", entry)
	}
	entries, err := svc.List(ctx, "/")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "dir" || entries[0].Path != "/dir" {
		t.Fatalf("list root = %+v, want dir entry", entries)
	}
	// A one-edit near miss becomes an actionable hint instead of a bare not-found.
	_, err = svc.Stat(ctx, "/di")
	if err == nil || !strings.Contains(err.Error(), `did you mean "dir"`) {
		t.Fatalf("stat near miss = %v, want did-you-mean hint", err)
	}
	if _, err := svc.Stat(ctx, "/missing"); err == nil || !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("stat missing = %v, want not found", err)
	}

	if err := svc.Create(ctx, "/dir/file.txt"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, err := svc.WriteAt(ctx, "/dir/file.txt", 0, []byte("hello world")); err != nil || n != 11 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if err := svc.Flush(ctx, "/dir/file.txt"); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := svc.WaitPath(ctx, "/dir/file.txt", 5*time.Second); err != nil {
		t.Fatalf("wait path: %v", err)
	}
	if err := svc.WaitIdle(ctx, 5*time.Second); err != nil {
		t.Fatalf("wait idle: %v", err)
	}
	if pending, err := svc.PendingUploads(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending uploads = %v, %v; want drained", pending, err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "dir", "file.txt")); err != nil || string(data) != "hello world" {
		t.Fatalf("remote content = %q, %v", data, err)
	}
	stamped := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if err := svc.SetModTime(ctx, "/dir/file.txt", stamped); err != nil {
		t.Fatalf("set modtime: %v", err)
	}
	if entry, err := svc.Stat(ctx, "/dir/file.txt"); err != nil || !entry.ModTime.Equal(stamped) {
		t.Fatalf("stat after set modtime = %+v, %v; want %s", entry, err, stamped)
	}

	stream, err := svc.ReadStream(ctx, "/dir/file.txt", 0, 5, false)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	data, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("read stream = %q, %v", data, err)
	}

	if _, err := svc.Space(ctx); err != nil && !errors.Is(err, drive.ErrSpaceUnsupported) {
		t.Fatalf("space: %v", err)
	}
	if _, err := svc.MountSpaces(ctx); err != nil && !errors.Is(err, drive.ErrSpaceUnsupported) {
		t.Fatalf("mount spaces: %v", err)
	}

	// Direct copy goes through the driver, then refreshes the owner's listing.
	if _, err := svc.Copy(ctx, control.FSCopyRequest{Source: "/dir/file.txt", Destination: "/dir/copy.txt"}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "dir", "copy.txt")); err != nil || string(data) != "hello world" {
		t.Fatalf("copied content = %q, %v", data, err)
	}
	copied, err := svc.Stat(ctx, "/dir/copy.txt")
	if err != nil || copied.Size != int64(len("hello world")) {
		t.Fatalf("stat copied = %+v, %v", copied, err)
	}

	if err := svc.Truncate(ctx, "/dir/copy.txt", 5); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if copied, err = svc.Stat(ctx, "/dir/copy.txt"); err != nil || copied.Size != 5 {
		t.Fatalf("stat truncated = %+v, %v", copied, err)
	}
	if err := svc.Rename(ctx, "/dir/copy.txt", "/dir/moved.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	resolved, err := svc.Resolve(ctx, "/dir/moved.txt")
	if err != nil || resolved.Path != "/dir/moved.txt" || resolved.IsDir {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if err := svc.InvalidateList(ctx, "/dir"); err != nil {
		t.Fatalf("invalidate list: %v", err)
	}
	if err := svc.Remove(ctx, "/dir/moved.txt", false); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := svc.WaitIdle(ctx, 5*time.Second); err != nil {
		t.Fatalf("wait idle after remove: %v", err)
	}
	if err := svc.Remove(ctx, "/dir/file.txt", false); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := svc.WaitIdle(ctx, 5*time.Second); err != nil {
		t.Fatalf("wait idle before rmdir: %v", err)
	}
	if err := svc.Remove(ctx, "/dir", true); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := svc.Stat(ctx, "/dir"); !errors.Is(err, drive.ErrNotFound) {
		t.Fatalf("stat removed dir = %v, want not found", err)
	}
}
