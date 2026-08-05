package core

import (
	"context"
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
	ctx := context.Background()
	drv := &completedBlockingStreamDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), CacheMaxBytes: 10 << 20, UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fs.Start(ctx)
	c := &Core{fs: fs}

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
	time.Sleep(1500 * time.Millisecond)

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
}
