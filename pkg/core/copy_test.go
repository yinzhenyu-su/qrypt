package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

func TestCreateTaskCopyFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "source.txt"), []byte("copy me"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, copyTaskRequest("/source.txt", "/copy.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeCopy || item.State != task.StateSucceeded || item.Progress.ItemsDone != 1 {
		t.Fatalf("task = %+v, want succeeded copy", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "copy.txt")); err != nil || string(data) != "copy me" {
		t.Fatalf("copy data = %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(remote, "source.txt")); err != nil {
		t.Fatalf("source missing after copy: %v", err)
	}
}

func TestCreateTaskCopyDirectoryRecursiveToRenamedDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRemote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcRemote, "dir", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "nested", "file.txt"), []byte("recursive"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "src-cache")})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "dst-cache")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, copyTaskRequest("/src/dir", "/dst/renamed", false, true))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.ItemsTotal != 2 || item.Progress.TransferBytesDone != int64(len("rootrecursive")) {
		t.Fatalf("task = %+v, want recursive copy success", item)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "root.txt")); err != nil || string(data) != "root" {
		t.Fatalf("copied root data = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "nested", "file.txt")); err != nil || string(data) != "recursive" {
		t.Fatalf("copied nested data = %q err=%v", data, err)
	}
	if info, err := os.Stat(filepath.Join(dstRemote, "renamed", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("copied empty dir stat = %+v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "dir", "nested", "file.txt")); err != nil {
		t.Fatalf("source should remain after copy: %v", err)
	}
}

func TestCreateTaskCopyBatchPartialFailed(t *testing.T) {
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

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeCopy,
		Items: []task.Item{
			{SourcePath: "/ok.txt", DestPath: "/ok-copy.txt"},
			{SourcePath: "/missing.txt", DestPath: "/missing-copy.txt"},
		},
		Options: task.Options{Concurrency: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 1 || item.Error == nil {
		t.Fatalf("task = %+v, want partial failed copy batch", item)
	}
	if item.Detail["concurrency"] != 2 {
		t.Fatalf("task detail = %+v, want concurrency 2", item.Detail)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "ok-copy.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("copied ok data = %q err=%v", data, err)
	}
}

func copyTaskRequest(sourcePath, destPath string, overwrite, recursive bool) task.Request {
	return task.Request{
		Type: task.TypeCopy,
		Items: []task.Item{{
			SourcePath: sourcePath,
			DestPath:   destPath,
		}},
		Options: task.Options{
			Overwrite: overwrite,
			Recursive: recursive,
		},
	}
}
