package core

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// assertFailPauseSticky re-checks an item after several progress ticks. The
// cloud ticker runs concurrently with the caller, so a Fail that only survives
// until the next tick is one the caller cannot rely on: resuming also revokes
// the capability CommitStagedUploadItem needs, so the "interrupt, then commit
// the staged bytes" recovery path fails with "item is running". This is the
// assertion that pins the ticker honouring the caller's pause.
func assertFailPauseSticky(t *testing.T, c *Core, taskID, itemID string) task.ItemResult {
	t.Helper()
	time.Sleep(10 * UploadStreamTaskPollInterval)
	items, err := c.ListTaskItems(context.Background(), taskID, task.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != itemID {
		t.Fatalf("items after the pause = %+v, want the failed item still listed", items)
	}
	if items[0].State != task.StateWaitingInput {
		t.Fatalf("item state after the pause = %s, want waiting_input: the progress ticker resumed an item the caller failed", items[0].State)
	}
	return items[0]
}

func TestCreateTaskUploadStreamBatchWritesAndFinishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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

func TestRecoveredUploadStreamItemKeepsPersistedProgress(t *testing.T) {
	t.Parallel()
	item := &uploadStreamItem{ID: "item", Size: 100}
	applyPersistedUploadStreamResult(item, task.ItemResult{
		State:            task.StateRunning,
		SourceBytesDone:  20,
		StagingBytesDone: 25,
		CloudBytesDone:   10,
		CloudBytesTotal:  100,
		Phase:            "upload",
		RemoteID:         "remote-id",
		Error:            &task.Error{Message: "retrying", Retryable: true},
	})
	if item.SourceRead != 20 || item.Written != 25 || item.CloudWritten != 10 ||
		item.CloudTotal != 100 || item.CloudPhase != "upload" || item.RemoteID != "remote-id" {
		t.Fatalf("recovered progress = %+v, want persisted progress", item)
	}
	if item.Error == nil || !item.Error.Retryable {
		t.Fatalf("recovered error = %+v, want retryable error", item.Error)
	}
}

func TestUploadStreamTaskSourcePathsIncludesSingleVFSFallback(t *testing.T) {
	t.Parallel()
	got := uploadStreamTaskSourcePaths(&vfs.VFS{}, "/quark-test/n_xq0LMgE_iVtPN9.mp4")
	if len(got) != 2 || got[0] != "/quark-test/n_xq0LMgE_iVtPN9.mp4" || got[1] != "/n_xq0LMgE_iVtPN9.mp4" {
		t.Fatalf("task source paths = %#v, want namespaced and mount-local paths", got)
	}
}

func TestPutUploadStreamRejectsDuplicateTaskID(t *testing.T) {
	t.Parallel()
	c := &Core{}
	first := &uploadStreamBatch{taskID: "upload-1"}
	second := &uploadStreamBatch{taskID: "upload-1"}
	if !c.putUploadStream(first) {
		t.Fatal("first upload stream registration rejected")
	}
	if c.putUploadStream(second) {
		t.Fatal("duplicate upload stream registration accepted")
	}
	if got := c.getUploadStream("upload-1"); got != first {
		t.Fatalf("registered batch = %p, want first batch %p", got, first)
	}
}

func TestCommitCompleteStagingWithoutReopeningSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond
	payload := []byte("already staged")

	created, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/staged.txt", Size: int64(len(payload))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, created.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, payload); err != nil || n != len(payload) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := handle.Fail("interrupted", "commit was interrupted"); err != nil {
		t.Fatal(err)
	}
	waiting, err := c.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != task.StateWaitingInput {
		t.Fatalf("task state = %s, want waiting_input", waiting.State)
	}
	items, err := c.ListTaskItems(ctx, created.ID, task.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Capabilities.CommitInput || items[0].Capabilities.OpenInput {
		t.Fatalf("item capabilities = %+v, want commit without input", items)
	}
	// The pause must outlast the ticker: it is the window in which
	// CommitStagedUploadItem below is the only legal next call.
	paused := assertFailPauseSticky(t, c, created.ID, "item")
	if !paused.Capabilities.CommitInput || paused.Capabilities.OpenInput {
		t.Fatalf("item capabilities after the pause = %+v, want commit without input", paused)
	}
	if paused.Error == nil || paused.Error.Code != "interrupted" {
		t.Fatalf("item error after the pause = %+v, want the caller's failure preserved", paused.Error)
	}
	if err := c.CommitStagedUploadItem(ctx, created.ID, "item"); err != nil {
		t.Fatal(err)
	}
	finished := waitCoreTask(t, c, created.ID)
	if finished.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want succeeded", finished)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "staged.txt")); err != nil || string(data) != string(payload) {
		t.Fatalf("remote data = %q err=%v", data, err)
	}
}

func TestUploadStreamBatchWaitingInputDismissable(t *testing.T) {
	// 任务在任意阶段都可删除：waiting_input（staging 未开始写）时
	// DismissTask 应取消并移除任务，而不是报 "not terminal"。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

	created, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/dismiss.txt", Size: 4}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, err := c.GetTask(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cur.State == task.StateWaitingInput {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task state = %s, want waiting_input", cur.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.DismissTask(ctx, created.ID); err != nil {
		t.Fatalf("DismissTask of waiting_input task: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, getErr := c.GetTask(ctx, created.ID)
		if errors.Is(getErr, task.ErrNotFound) {
			break
		}
		if getErr != nil {
			t.Fatal(getErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("task still present after cancellation cleanup, want removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries, err := os.ReadDir(remote)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "dismiss.txt" {
			t.Fatal("dismissed waiting_input task left a remote file")
		}
	}
}

func TestCommitIncompleteStagingStillRequiresSource(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	created, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/partial.txt", Size: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, created.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write(ctx, []byte("part")); err != nil {
		t.Fatal(err)
	}
	if err := handle.Fail("interrupted", "input interrupted"); err != nil {
		t.Fatal(err)
	}
	items, err := c.ListTaskItems(ctx, created.ID, task.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Capabilities.OpenInput || items[0].Capabilities.CommitInput {
		t.Fatalf("item capabilities = %+v, want source input", items)
	}
	if err := c.CommitStagedUploadItem(ctx, created.ID, "item"); err == nil {
		t.Fatal("commit incomplete staging succeeded")
	}
}

func TestCreateTaskUploadStreamBatchUsesDefaultDestination(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{Name: "cloud", RootID: remote, StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "cloud", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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

func TestApplyRemoteUploadStateKeepsParentRunningForRetryableFailure(t *testing.T) {
	t.Parallel()

	item := &uploadStreamItem{
		ID:    "item",
		State: task.StateRunning,
	}
	remote := task.Task{
		ID:    "remote-1",
		State: task.StateFailed,
		Error: &task.Error{Message: "stale upload session", Retryable: true},
		Progress: task.Progress{
			Phase: "failed",
		},
	}

	if dismiss := applyRemoteUploadState(item, remote); dismiss {
		t.Fatal("retryable remote failure requested dismissal")
	}
	if item.State != task.StateRunning {
		t.Fatalf("parent item state = %s, want running", item.State)
	}
	if item.Error != nil {
		t.Fatalf("parent item error = %+v, want nil while remote retry is pending", item.Error)
	}
}

func TestUploadStreamItemFailWaitsForReopen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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
	// Only 3 of 5 bytes are staged, so the pause must keep offering input
	// rather than resume the upload: the caller is expected to reopen below.
	paused := assertFailPauseSticky(t, c, item.ID, "item")
	if !paused.Capabilities.OpenInput || paused.ResumeOffset != int64(len("str")) {
		t.Fatalf("item after the pause = %+v, want open-input at the staged offset", paused)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

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

// completedBlockingStreamDriver mirrors real drivers (e.g. 115) that report
// UploadPhaseCompleted as soon as the OSS upload returns, before any
// post-upload verification (waitUploadedFile) finishes. The internal upload
// task is therefore briefly reported as succeeded while the engine is still
// running and the pending record is still present — the exact window in which
// the upload-stream-task poller used to dismiss (and thereby cancel) the
// upload and lose the freshly uploaded remote file.
type completedBlockingStreamDriver struct {
	drive.UnsupportedOperations
	mu      sync.Mutex
	uploads int
	entries map[string]drive.Entry
	removed []string
	entered chan struct{}
	release chan struct{}
}

func (d *completedBlockingStreamDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}
func (d *completedBlockingStreamDriver) Init(context.Context) error { return nil }
func (d *completedBlockingStreamDriver) Drop(context.Context) error { return nil }
func (d *completedBlockingStreamDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "completed-blocking-stream", Health: drive.HealthLevelOK}, nil
}
func (d *completedBlockingStreamDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *completedBlockingStreamDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *completedBlockingStreamDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []drive.Entry
	for _, entry := range d.entries {
		if entry.ParentID == parentID {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
func (d *completedBlockingStreamDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *completedBlockingStreamDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	f, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	if _, err := io.Copy(io.Discard, f); err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	d.mu.Lock()
	d.uploads++
	id := req.Name + "-" + strconv.Itoa(d.uploads)
	entry := drive.Entry{ID: id, ParentID: req.ParentID, Name: req.Name, Size: req.Source.Size()}
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	d.entries[id] = entry
	d.mu.Unlock()
	d.entered <- struct{}{}
	<-d.release
	return entry, nil
}
func (d *completedBlockingStreamDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed = append(d.removed, entry.ID)
	return nil
}
func (d *completedBlockingStreamDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}
func (d *completedBlockingStreamDriver) remoteCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}

// TestUploadStreamTaskPollerDismissKeepsUploadedFile is the end-to-end
// regression test for the reported bug where a file that finished uploading
// disappeared after a directory refresh. It drives the real production path
// (upload stream task -> VFS pending upload -> poller -> DismissTask) with a
// driver that reports the completed phase and then blocks, so the poller
// observes a "succeeded but still active" upload task. The upload must not be
// canceled by that dismiss, and the freshly uploaded remote file must survive.
func TestUploadStreamTaskPollerDismissKeepsUploadedFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storage := filepath.Join(t.TempDir(), "cache")
	drv := &completedBlockingStreamDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: storage, CacheMaxBytes: 10 << 20, UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	UploadStreamTaskPollInterval = 5 * time.Millisecond

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadStreamBatch,
		Items: []task.Item{{ItemID: "item", DestPath: "/keep.txt", Size: int64(len("payload"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.OpenUploadStreamItem(ctx, item.ID, "item")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := handle.Write(ctx, []byte("payload")); err != nil || n != len("payload") {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := handle.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The driver's PutSource reports the completed phase and blocks, holding
	// the upload task in "succeeded but still active" state.
	select {
	case <-drv.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("driver PutSource did not start")
	}

	// Give the stream task's 500ms poller enough ticks to run its DismissTask
	// on the succeeded-but-active internal upload.
	time.Sleep(50 * time.Millisecond)

	// The pending record must have survived the poller dismisses.
	if pendings := fs.PendingUploads(); len(pendings) != 1 {
		t.Fatalf("pending after poller dismiss = %+v, want 1", pendings)
	}

	// Let the engine finish the upload normally, then wait for the stream task.
	close(drv.release)
	final := waitCoreTask(t, c, item.ID)
	if final.State != task.StateSucceeded {
		t.Fatalf("stream task state = %s, want succeeded (task=%+v)", final.State, final)
	}

	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("uploaded file was removed from the remote: %v", removed)
	}
	if got := drv.remoteCount(); got != 1 {
		t.Fatalf("remote entries = %d, want 1 (uploaded file must survive)", got)
	}

	// The poller marks the task succeeded as soon as the upload commits, but
	// the worker removes the staging file slightly after; wait for the
	// staging directory to drain so the test TempDir cleanup does not race
	// the removal.
	stagingDir := filepath.Join(storage, "staging")
	cleanupDeadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(stagingDir)
		if err == nil && len(entries) == 0 {
			break
		}
		if time.Now().After(cleanupDeadline) {
			t.Fatalf("staging never cleared in %s: %v", stagingDir, entries)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
