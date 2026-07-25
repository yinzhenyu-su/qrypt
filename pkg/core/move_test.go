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

func TestCreateMoveTaskSameMountRenamesPath(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("same mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateMoveTask(ctx, MoveTaskRequest{SourcePath: "/old.txt", DestPath: "/new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Type != task.TypeMoveRemote || item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded move_remote", item)
	}
	if _, err := os.Stat(filepath.Join(remote, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old path still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "new.txt")); err != nil || string(data) != "same mount" {
		t.Fatalf("new data = %q err=%v", data, err)
	}
	tasks, err := c.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeMoveRemote}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != item.ID {
		t.Fatalf("tasks = %+v, want created move task", tasks)
	}
}

func TestCreateMoveTaskCrossQryptMountCopiesThenDeletesSource(t *testing.T) {
	ctx := context.Background()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("cross mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "src-cache")})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "dst-cache")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateMoveTask(ctx, MoveTaskRequest{SourcePath: "/src/file.txt", DestPath: "/dst/file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if item.State != task.StateSucceeded || item.Detail["mode"] != "copy_delete" {
		t.Fatalf("task = %+v, want copy_delete success", item)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "file.txt")); err != nil || string(data) != "cross mount" {
		t.Fatalf("dest data = %q err=%v", data, err)
	}
}

func TestCreateMoveTaskCrossQryptMountRejectsDirectoryWithoutRecursive(t *testing.T) {
	ctx := context.Background()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.Mkdir(filepath.Join(srcRemote, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "src-cache")})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{CacheDir: filepath.Join(t.TempDir(), "dst-cache")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateMoveTask(ctx, MoveTaskRequest{SourcePath: "/src/dir", DestPath: "/dst/dir"})
	if err != nil {
		t.Fatal(err)
	}
	if item.State != task.StateFailed || item.LastError == "" {
		t.Fatalf("task = %+v, want failed task", item)
	}
}
