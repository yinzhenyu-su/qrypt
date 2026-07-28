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

func TestCreateTaskUploadStreamBatchWritesAndFinishes(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamBatch,
		Items: []task.Item{
			{ItemID: "a", DestPath: "/a.txt", Size: int64(len("alpha"))},
			{ItemID: "b", DestPath: "/b.txt", Size: int64(len("beta"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		data string
	}{
		{id: "a", data: "alpha"},
		{id: "b", data: "beta"},
	} {
		handle, err := c.OpenUploadStreamItem(ctx, item.ID, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		n, err := handle.Write(ctx, []byte(tc.data))
		if err != nil {
			t.Fatal(err)
		}
		if n != len(tc.data) {
			t.Fatalf("Write(%s) n=%d, want %d", tc.id, n, len(tc.data))
		}
		if err := handle.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.StagingBytesDone != int64(len("alphabeta")) {
		t.Fatalf("task = %+v, want stream upload batch success", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "a.txt")); err != nil || string(data) != "alpha" {
		t.Fatalf("remote a data = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "b.txt")); err != nil || string(data) != "beta" {
		t.Fatalf("remote b data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadStreamBatchUsesDefaultDestination(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{Name: "cloud", RootID: remote, StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "cloud", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start(ctx)
	c := &Core{fs: ns, defaultUploadMount: "cloud", defaultUploadPath: "/Inbox"}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamBatch,
		Items: []task.Item{{
			ItemID:   "one",
			DestPath: "upload.txt",
			Size:     int64(len("stream default")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write(ctx, []byte("stream default")); err != nil {
		t.Fatal(err)
	}
	if err := handle.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Path != "/cloud/Inbox/upload.txt" {
		t.Fatalf("task = %+v, want default stream destination", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "Inbox", "upload.txt")); err != nil || string(data) != "stream default" {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadStreamBatchConflictPolicySkipExisting(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamBatch,
		Items: []task.Item{{
			ItemID:   "one",
			DestPath: "/existing.txt",
			Size:     int64(len("local")),
		}},
		Options: task.Options{ConflictPolicy: "skip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || len(item.Result.Items) != 1 || item.Result.Items[0].Phase != "skipped" {
		t.Fatalf("task = %+v, want skipped success", item)
	}
	if _, err := c.OpenUploadStreamItem(ctx, item.ID, "one"); err == nil {
		t.Fatal("OpenUploadStreamItem succeeded for skipped item, want error")
	}
	if data, err := os.ReadFile(filepath.Join(remote, "existing.txt")); err != nil || string(data) != "remote" {
		t.Fatalf("remote existing data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadStreamBatchConflictPolicyFailExisting(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadStreamBatch,
		Items: []task.Item{{
			ItemID:   "one",
			DestPath: "/existing.txt",
			Size:     int64(len("local")),
		}},
		Options: task.Options{ConflictPolicy: "fail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || len(item.Result.Items) != 1 || item.Result.Items[0].Error == nil {
		t.Fatalf("task = %+v, want conflict failure", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "existing.txt")); err != nil || string(data) != "remote" {
		t.Fatalf("remote existing data = %q err=%v", data, err)
	}
}

func TestUploadStreamItemCommitDoesNotWaitForRemoteUpload(t *testing.T) {
	ctx := context.Background()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/file.txt", Size: int64(len("data"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("data")); err != nil || n != len("data") {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := handle.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetTask(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.StateRunning || got.Progress.ItemsDone != 0 || got.Progress.StagingBytesDone != int64(len("data")) {
		t.Fatalf("task after commit = %+v, want running with staged bytes", got)
	}
	if len(got.Result.Items) != 1 || got.Result.Items[0].State != task.StateRunning {
		t.Fatalf("result after commit = %+v, want running item", got.Result.Items)
	}
}

func TestUploadStreamItemFailWaitsForReopen(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/file.txt", Size: int64(len("stream"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("str")); err != nil || n != len("str") {
		t.Fatalf("first write n=%d err=%v", n, err)
	}
	if err := handle.Fail("input_stream_failed", "source permission lost"); err != nil {
		t.Fatal(err)
	}
	waitForCoreTaskPhase(t, c, item.ID, string(task.StateWaitingInput))
	waitingItems, err := c.ListTaskItems(ctx, item.ID, task.ItemFilter{States: []task.State{task.StateWaitingInput}})
	if err != nil {
		t.Fatal(err)
	}
	if len(waitingItems) != 1 || waitingItems[0].ItemID != "item" || waitingItems[0].ResumeOffset != int64(len("str")) {
		t.Fatalf("waiting items = %+v, want resume offset after first write", waitingItems)
	}
	one, err := c.GetTaskItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if one.State != task.StateWaitingInput || one.ResumeOffset != int64(len("str")) {
		t.Fatalf("task item = %+v, want waiting input resume state", one)
	}

	handle, err = c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("eam")); err != nil || n != len("eam") {
		t.Fatalf("resume write n=%d err=%v", n, err)
	}
	if err := handle.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := waitCoreTask(t, c, item.ID)
	if got.State != task.StateSucceeded || got.Progress.StagingBytesDone != int64(len("stream")) {
		t.Fatalf("task = %+v, want resumed success", got)
	}
	if len(got.Result.Items) != 1 || got.Result.Items[0].ResumeOffset != int64(len("stream")) {
		t.Fatalf("result items = %+v", got.Result.Items)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "file.txt")); err != nil || string(data) != "stream" {
		t.Fatalf("remote data = %q err=%v", data, err)
	}
}

func TestUploadStreamTaskCancelRemovesUncommittedStaging(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/cancel.txt", Size: int64(len("partial"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("partial")); err != nil || n != len("partial") {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := c.CancelTask(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	got := waitCoreTask(t, c, item.ID)
	if got.State != task.StateCanceled {
		t.Fatalf("task = %+v, want canceled", got)
	}
	if _, err := c.Stat(ctx, "/cancel.txt"); err == nil {
		t.Fatal("Stat(/cancel.txt) succeeded after cancel, want staging removed")
	}
}

func TestUploadStreamTaskCancelItemRemovesStaging(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/cancel-item.txt", Size: int64(len("partial"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.GetTaskItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Capabilities.OpenInput || !result.Capabilities.Cancelable {
		t.Fatalf("initial item capabilities = %+v, want open input and cancelable", result.Capabilities)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("partial")); err != nil || n != len("partial") {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := c.CancelTaskItem(ctx, item.ID, "item"); err != nil {
		t.Fatal(err)
	}
	got := waitCoreTask(t, c, item.ID)
	if got.State != task.StateFailed || len(got.Result.Items) != 1 || got.Result.Items[0].State != task.StateCanceled {
		t.Fatalf("task = %+v, want failed task with canceled item", got)
	}
	if got.Result.Items[0].Capabilities.Cancelable {
		t.Fatalf("canceled item capabilities = %+v, want not cancelable", got.Result.Items[0].Capabilities)
	}
	if _, err := c.Stat(ctx, "/cancel-item.txt"); err == nil {
		t.Fatal("Stat(/cancel-item.txt) succeeded after item cancel, want staging removed")
	}
	if _, err := handle.Write(ctx, []byte("again")); err == nil {
		t.Fatal("Write after item cancel succeeded, want error")
	}
}
