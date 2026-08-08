package vfs_test

import (
	"context"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

// completedBlockingUploadDriver mirrors real drivers (e.g. 115) that report
// UploadPhaseCompleted as soon as the OSS upload returns, before any
// post-upload verification (waitUploadedFile) finishes. The upload task is
// therefore briefly reported as succeeded while the engine is still running
// and the pending record is still present.
type completedBlockingUploadDriver struct {
	drive.UnsupportedOperations
	mu      sync.Mutex
	uploads int
	entries map[string]drive.Entry
	removed []string
	entered chan struct{}
	release chan struct{}
}

func (d *completedBlockingUploadDriver) Capabilities() []drive.Capability {
	return []drive.Capability{drive.CapabilitySourceUploader, drive.CapabilityWriter}
}
func (d *completedBlockingUploadDriver) Init(context.Context) error { return nil }
func (d *completedBlockingUploadDriver) Drop(context.Context) error { return nil }
func (d *completedBlockingUploadDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return testDriverSnapshot("completed-blocking-upload"), nil
}
func (d *completedBlockingUploadDriver) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}
func (d *completedBlockingUploadDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}
func (d *completedBlockingUploadDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
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
func (d *completedBlockingUploadDriver) Read(context.Context, drive.Entry, int64, int64) (io.ReadCloser, error) {
	return nil, io.EOF
}
func (d *completedBlockingUploadDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	f, err := source.Open(ctx)
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
	id := name + "-" + strconv.Itoa(d.uploads)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, Size: source.Size()}
	if d.entries == nil {
		d.entries = map[string]drive.Entry{}
	}
	d.entries[id] = entry
	d.mu.Unlock()
	d.entered <- struct{}{}
	<-d.release
	return entry, nil
}
func (d *completedBlockingUploadDriver) Remove(_ context.Context, entry drive.Entry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removed = append(d.removed, entry.ID)
	return nil
}
func (d *completedBlockingUploadDriver) uploadCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.uploads
}
func (d *completedBlockingUploadDriver) removedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.removed...)
}

// TestVFSUploadTaskDismissDuringActiveCompletedUploadKeepsFile reproduces the
// regression where the upload-stream-task poller dismissed an internal upload
// task that already reported "completed" but was still active (the engine was
// waiting on the driver's post-upload verification). Dismiss must not cancel
// the in-flight upload: cancel-and-remove would delete the pending record and
// the engine would then remove the freshly uploaded remote file.
func TestVFSUploadTaskDismissDuringActiveCompletedUploadKeepsFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &completedBlockingUploadDriver{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/keep.txt", []byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/keep.txt"); err != nil {
		t.Fatal(err)
	}

	// Wait until the driver's PutSource reports the completed phase and
	// blocks: the upload task is active and already reported succeeded, but
	// the pending record still exists.
	select {
	case <-drv.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("driver PutSource did not start")
	}

	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeUploadRemote}, Path: "/keep.txt"})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one active upload", tasks)
	}
	if tasks[0].State != task.StateSucceeded {
		t.Fatalf("task state = %s, want succeeded (driver reported completed phase)", tasks[0].State)
	}
	if err := fs.DismissTask(ctx, tasks[0].ID); err != nil {
		t.Fatalf("DismissTask = %v", err)
	}

	// The pending record must survive the dismiss.
	if pendings := fs.PendingUploads(); len(pendings) != 1 {
		t.Fatalf("pending after dismiss = %+v, want 1", pendings)
	}

	// Let the engine finish the upload normally.
	close(drv.release)
	waitNoPending(t, fs)

	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
	if removed := drv.removedIDs(); len(removed) != 0 {
		t.Fatalf("uploaded file was removed: %v", removed)
	}
	entries, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "keep.txt" {
		t.Fatalf("entries = %+v, want keep.txt to remain visible", entries)
	}
}
