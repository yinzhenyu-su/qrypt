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

func TestCreateTaskDownloadStreamBatchReadsAckAndFinishes(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "b.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeDownloadStreamBatch,
		Items: []task.Item{
			{ItemID: "a", SourcePath: "/a.txt"},
			{ItemID: "b", SourcePath: "/b.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	for _, id := range []string{"a", "b"} {
		handle, err := c.OpenDownloadStreamItem(ctx, item.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		n, err := handle.ReadInto(ctx, buf)
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Ack(int64(n)); err != nil {
			t.Fatal(err)
		}
		if n, err := handle.ReadInto(ctx, buf); err != nil || n != 0 {
			t.Fatalf("second read n=%d err=%v, want eof", n, err)
		}
		if err := handle.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Progress.ItemsDone != 2 || item.Progress.OutputBytesDone != int64(len("alphabeta")) {
		t.Fatalf("task = %+v, want stream batch success", item)
	}
	if len(item.Result.Items) != 2 || item.Result.Items[0].ItemID != "a" || item.Result.Items[0].ResumeOffset != int64(len("alpha")) {
		t.Fatalf("result items = %+v", item.Result.Items)
	}
}

func waitForCoreTaskPhase(t *testing.T, c *Core, id, phase string) task.Task {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := c.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Progress.Phase == phase {
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	item, err := c.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task %s did not reach phase %s, last phase=%s state=%s", id, phase, item.Progress.Phase, item.State)
	return task.Task{}
}

func TestDownloadStreamItemFailWaitsForReopen(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeDownloadStreamBatch,
		Items: []task.Item{{ItemID: "item", SourcePath: "/file.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenDownloadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	n, err := handle.ReadInto(ctx, buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Ack(int64(n)); err != nil {
		t.Fatal(err)
	}
	if err := handle.Fail("output_stream_failed", "target removed"); err != nil {
		t.Fatal(err)
	}
	waitForCoreTaskPhase(t, c, item.ID, string(task.StateWaitingOutput))

	handle, err = c.OpenDownloadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	n, err = handle.ReadInto(ctx, buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "eam" {
		t.Fatalf("resumed data = %q, want eam", string(buf[:n]))
	}
	if err := handle.Ack(int64(n)); err != nil {
		t.Fatal(err)
	}
	if err := handle.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := waitCoreTask(t, c, item.ID)
	if got.State != task.StateSucceeded || got.Progress.OutputBytesDone != int64(len("stream")) {
		t.Fatalf("task = %+v, want resumed success", got)
	}
}

func TestDownloadStreamTaskCancelItem(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeDownloadStreamBatch,
		Items: []task.Item{{ItemID: "item", SourcePath: "/file.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.GetTaskItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Capabilities.OpenOutput || !result.Capabilities.Cancelable {
		t.Fatalf("initial item capabilities = %+v, want open output and cancelable", result.Capabilities)
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
	if _, err := c.OpenDownloadStreamItem(ctx, item.ID, "item"); err == nil {
		t.Fatal("OpenDownloadStreamItem after cancel succeeded, want error")
	}
}
