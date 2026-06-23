package vfs_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/localfs"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestVFSStagesUploadsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	if err := raw.Init(ctx); err != nil {
		t.Fatal(err)
	}

	fs, err := vfs.New(raw, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/hello.txt", []byte("hello qrypt"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/hello.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rc, err := fs.Read(ctx, "/hello.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello qrypt" {
		t.Fatalf("unexpected data: %q", data)
	}
}

func TestVFSRecoversPendingUploads(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()

	first, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAt(ctx, "/resume.txt", []byte("resume me"), 0); err != nil {
		t.Fatal(err)
	}
	if len(first.Pending()) != 1 {
		t.Fatalf("expected one pending file, got %d", len(first.Pending()))
	}

	second, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	second.Start(ctx)
	waitNoPending(t, second)

	data, err := os.ReadFile(remote + "/resume.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "resume me" {
		t.Fatalf("unexpected recovered data: %q", data)
	}
}

func TestEncryptedDriverRoundTrip(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	cache := t.TempDir()
	raw := localfs.New(remote)
	cp, err := crypt.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	drv := crypt.NewDriver(raw, cp)
	fs, err := vfs.New(drv, vfs.Options{CacheDir: cache, CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/secret.txt", bytes.Repeat([]byte("a"), 80*1024), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/secret.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	rawEntries, err := raw.List(ctx, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(rawEntries) != 1 {
		t.Fatalf("expected one raw encrypted entry, got %d", len(rawEntries))
	}
	if rawEntries[0].Name == "secret.txt" {
		t.Fatal("expected encrypted filename on raw backend")
	}
	info, err := fs.Stat(ctx, "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 80*1024 {
		t.Fatalf("expected plaintext size, got %d", info.Size)
	}

	rc, err := fs.Read(ctx, "/secret.txt", 64*1024, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected encrypted read data: %q", data)
	}
}

func TestVFSRenameUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/old.txt", []byte("rename me"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/old.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	if err := fs.Rename(ctx, "/old.txt", "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote + "/old.txt"); !os.IsNotExist(err) {
		t.Fatalf("old file should not exist, err=%v", err)
	}
	data, err := os.ReadFile(remote + "/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rename me" {
		t.Fatalf("unexpected renamed data: %q", data)
	}
}

func TestVFSRenamePendingFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("pending rename"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(ctx, "/draft.txt", "/final.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/final.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	data, err := os.ReadFile(remote + "/final.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "pending rename" {
		t.Fatalf("unexpected pending renamed data: %q", data)
	}
}

func TestVFSTruncateUploadedFile(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 10 << 20})
	if err != nil {
		t.Fatal(err)
	}
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

func waitNoPending(t *testing.T, fs vfs.FileSystem) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fs.Pending()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending uploads did not drain: %+v", fs.Pending())
}

var _ drive.Driver = (*localfs.Driver)(nil)
