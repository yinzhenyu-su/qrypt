package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCreateTaskDownloadFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "source.txt"), []byte("download me"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	dest := filepath.Join(t.TempDir(), "downloads", "source.txt")

	item, err := c.CreateTask(ctx, downloadTaskRequest("/source.txt", dest, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeDownload || item.State != task.StateSucceeded || item.Progress.ItemsDone != 1 || item.Progress.OutputBytesDone != int64(len("download me")) {
		t.Fatalf("task = %+v, want download success", item)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "download me" {
		t.Fatalf("downloaded data = %q err=%v", data, err)
	}
}

func TestCreateTaskDownloadRejectsExistingDestinationWithoutOverwrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "source.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	dest := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(dest, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}

	item, err := c.CreateTask(ctx, downloadTaskRequest("/source.txt", dest, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || item.Error == nil {
		t.Fatalf("task = %+v, want failed existing destination", item)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "local" {
		t.Fatalf("existing data = %q err=%v", data, err)
	}
}

func TestCreateTaskDownloadBatchPartialFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	destDir := t.TempDir()

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeDownload,
		Items: []task.Item{
			{SourcePath: "/ok.txt", DestPath: filepath.Join(destDir, "ok.txt")},
			{SourcePath: "/missing.txt", DestPath: filepath.Join(destDir, "missing.txt")},
		},
		Options: task.Options{Concurrency: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 1 || item.Error == nil {
		t.Fatalf("task = %+v, want partial failed download batch", item)
	}
	if item.Detail["concurrency"] != float64(2) && item.Detail["concurrency"] != 2 {
		t.Fatalf("task detail = %+v, want concurrency 2", item.Detail)
	}
	if data, err := os.ReadFile(filepath.Join(destDir, "ok.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("downloaded ok data = %q err=%v", data, err)
	}
}

func TestCreateTaskDownloadDirectoryRecursive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "dir", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "dir", "nested", "child.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	dest := filepath.Join(t.TempDir(), "downloaded-dir")

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeDownload,
		Items: []task.Item{{
			SourcePath: "/dir",
			DestPath:   dest,
		}},
		Options: task.Options{Recursive: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.ItemsTotal != 2 || item.Progress.OutputBytesDone != int64(len("rootchild")) {
		t.Fatalf("task = %+v, want recursive download success", item)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "root.txt")); err != nil || string(data) != "root" {
		t.Fatalf("downloaded root data = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "nested", "child.txt")); err != nil || string(data) != "child" {
		t.Fatalf("downloaded child data = %q err=%v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dest, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty dir stat = %+v err=%v", info, err)
	}
}

func downloadTaskRequest(sourcePath, destPath string, overwrite bool) task.Request {
	return task.Request{
		Type: task.TypeDownload,
		Items: []task.Item{{
			SourcePath: sourcePath,
			DestPath:   destPath,
		}},
		Options: task.Options{Overwrite: overwrite},
	}
}
