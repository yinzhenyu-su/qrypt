package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestCreateTaskDeleteBatchRemovesPaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{"a.txt": "a", "b.txt": "b", "ok.txt": "ok"}); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		DeleteDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeDeleteBatch,
		Items: []task.Item{
			{Path: "/a.txt"},
			{Path: "/b.txt"},
		},
		Options: task.Options{Concurrency: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 0 {
		t.Fatalf("task = %+v, want succeeded delete batch", item)
	}
	if item.Detail["concurrency"] != 2 {
		t.Fatalf("task detail = %+v, want concurrency 2", item.Detail)
	}
	if _, err := c.Stat(ctx, "/a.txt"); err == nil {
		t.Fatal("deleted path /a.txt is still visible")
	}
	if _, err := c.Stat(ctx, "/b.txt"); err == nil {
		t.Fatal("deleted path /b.txt is still visible")
	}
}

func TestCreateTaskDeleteBatchPartialFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{"a.txt": "a", "b.txt": "b", "ok.txt": "ok"}); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		DeleteDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeDeleteBatch,
		Items: []task.Item{
			{Path: "/ok.txt"},
			{Path: "/missing.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 1 || item.Error == nil {
		t.Fatalf("task = %+v, want partial_failed delete batch", item)
	}
	if len(item.Result.Items) != 2 || item.Result.Items[1].Path != "/missing.txt" || item.Result.Items[1].State != task.StateFailed || item.Result.Items[1].Error == nil {
		t.Fatalf("result items = %+v, want missing path failure", item.Result.Items)
	}
	if _, err := c.Stat(ctx, "/ok.txt"); err == nil {
		t.Fatal("deleted path /ok.txt is still visible")
	}
}

func TestCreateTaskDeleteBatchRecursiveDirectory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := drive.NewFakeDriver()
	if err := raw.Seed(map[string]string{
		"a.txt":                "a",
		"b.txt":                "b",
		"ok.txt":               "ok",
		"dir/root.txt":         "root",
		"dir/nested/child.txt": "child",
	}); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(raw, vfs.Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		DeleteDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type:    task.TypeDeleteBatch,
		Items:   []task.Item{{Path: "/dir"}},
		Options: task.Options{Recursive: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 4 || item.Progress.ItemsTotal != 4 {
		t.Fatalf("task = %+v, want recursive delete progress", item)
	}
	if _, err := c.Stat(ctx, "/dir"); err == nil {
		t.Fatal("deleted directory is still visible")
	}
}
