package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCreateTaskSameMountMoveRenamesPath(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("same mount"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, moveTaskRequest("/old.txt", "/new.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
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

func TestCreateTaskMoveRemoteRenamesPath(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "old.txt"), []byte("create task move"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, moveTaskRequest("/old.txt", "/new.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeMoveRemote || item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded move_remote", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "new.txt")); err != nil || string(data) != "create task move" {
		t.Fatalf("new data = %q err=%v", data, err)
	}
}

func TestCreateTaskCrossQryptMountCopiesThenDeletesSource(t *testing.T) {
	ctx := context.Background()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRemote, "file.txt"), []byte("cross mount"), 0o644); err != nil {
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
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/file.txt", "/dst/file.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
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

func TestCreateTaskCrossQryptMountRejectsDirectoryWithoutRecursive(t *testing.T) {
	ctx := context.Background()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.Mkdir(filepath.Join(srcRemote, "dir"), 0o755); err != nil {
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
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/dir", "/dst/dir", false, false))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || item.Error == nil || item.Error.Message == "" {
		t.Fatalf("task = %+v, want failed task", item)
	}
}

func TestCreateTaskCrossQryptMountMovesDirectoryToRenamedDestination(t *testing.T) {
	ctx := context.Background()
	srcRemote := t.TempDir()
	dstRemote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcRemote, "dir", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "root.txt"), []byte("root move"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "dir", "nested", "file.txt"), []byte("move dir"), 0o644); err != nil {
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
	ns.Start(ctx)
	c := &Core{fs: ns}

	item, err := c.CreateTask(ctx, moveTaskRequest("/src/dir", "/dst/renamed", false, true))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Detail["mode"] != "copy_delete" || item.Progress.ItemsDone != 2 || item.Progress.ItemsTotal != 2 {
		t.Fatalf("task = %+v, want renamed directory move success", item)
	}
	if _, err := os.Stat(filepath.Join(srcRemote, "dir")); !os.IsNotExist(err) {
		t.Fatalf("source dir still exists, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "nested", "file.txt")); err != nil || string(data) != "move dir" {
		t.Fatalf("moved nested data = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dstRemote, "renamed", "root.txt")); err != nil || string(data) != "root move" {
		t.Fatalf("moved root data = %q err=%v", data, err)
	}
}

func moveTaskRequest(sourcePath, destPath string, overwrite, recursive bool) task.Request {
	return task.Request{
		Type: task.TypeMoveRemote,
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

func waitCoreTask(t *testing.T, c *Core, id string) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := c.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch item.State {
		case task.StateSucceeded, task.StatePartialFailed, task.StateFailed, task.StateCanceled:
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := c.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task %s did not finish, last state=%s", id, item.State)
	return task.Task{}
}
