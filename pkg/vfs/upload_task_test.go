package vfs_test

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"strings"
	"testing"
	"time"
)

func TestVFSUploadTaskCancelRemovesPendingUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/cancel.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/cancel.txt"); err != nil {
		t.Fatal(err)
	}
	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeUploadRemote}})
	if len(tasks) != 1 || tasks[0].State != task.StateScheduled || !tasks[0].Capabilities.Cancelable {
		t.Fatalf("tasks = %+v, want one cancelable scheduled upload", tasks)
	}
	if err := fs.CancelTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none", pending)
	}
	if got := drv.uploadCount(); got != 0 {
		t.Fatalf("upload count = %d, want 0", got)
	}
}

func TestVFSUploadTaskRemoveClearsCompletedHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/done.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/done.txt"); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)

	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeUploadRemote}})
	if len(tasks) != 1 || tasks[0].State != task.StateSucceeded || !tasks[0].Capabilities.Dismissible || tasks[0].Capabilities.Cancelable {
		t.Fatalf("tasks = %+v, want one dismissible completed upload", tasks)
	}
	if err := fs.DismissTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	if tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeUploadRemote}}); len(tasks) != 0 {
		t.Fatalf("tasks after remove = %+v, want none", tasks)
	}
}

func TestVFSUploadTaskRetryRunsScheduledUploadNow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/retry-now.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/retry-now.txt"); err != nil {
		t.Fatal(err)
	}
	tasks := fs.Tasks(task.Filter{Types: []task.Type{task.TypeUploadRemote}, Path: "/retry-now.txt"})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %+v, want one upload", tasks)
	}
	if err := fs.RetryTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	waitNoPending(t, fs)
	if got := drv.uploadCount(); got != 1 {
		t.Fatalf("upload count = %d, want 1", got)
	}
}
func TestVFSDebugUploadCancelRequeuesAndRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &cancelAwareUploadDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay, UploadWorkers: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	content := []byte(strings.Repeat("resume-test", 128))
	if _, err := fs.WriteAt(ctx, "/resume-debug.bin", content, 0); err != nil {
		t.Fatal(err)
	}
	result, err := fs.DebugInjectUploadCancel(ctx, vfs.DebugUploadCancelRequest{
		Path:       "/resume-debug.bin",
		Phase:      drive.UploadPhaseUploading,
		AfterBytes: 1,
		Reason:     "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Armed || result.ID == "" {
		t.Fatalf("unexpected fault result: %+v", result)
	}
	if err := fs.Flush(ctx, "/resume-debug.bin"); err != nil {
		t.Fatal(err)
	}

	waitNoPending(t, fs)
	attempts, canceled := drv.state()
	if attempts < 2 {
		t.Fatalf("upload attempts = %d, want retry after debug cancel", attempts)
	}
	if !canceled {
		t.Fatal("driver did not observe context cancellation")
	}
	if faults := fs.DebugUploadCancelFaults(ctx); len(faults) != 0 {
		t.Fatalf("one-shot debug fault was not cleared: %+v", faults)
	}
	entry, err := fs.Stat(ctx, "/resume-debug.bin")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) {
		t.Fatalf("uploaded size = %d, want %d", entry.Size, len(content))
	}
}
func TestVFSUploadRetryUsesGrowingBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv := &countingUploadDriver{failUploads: 2}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, fs)
	fs.Start(ctx)

	if _, err := fs.WriteAt(ctx, "/retry.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/retry.txt"); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, func() bool {
		pending := fs.PendingUploads()
		return len(pending) == 1 && pending[0].RetryCount == 2
	})
	pending := fs.PendingUploads()[0]
	delay := time.Unix(0, pending.NextAttemptAt).Sub(time.Unix(0, pending.LastAttemptAt))
	if delay < 700*time.Millisecond {
		t.Fatalf("retry delay = %s, want exponential backoff after second failure", delay)
	}
}
func TestVFSResumePendingWaitsUntilNextAttempt(t *testing.T) {
	cacheDir := t.TempDir()
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDriver := &countingUploadDriver{failUploads: 1}
	first, err := vfs.New(firstDriver, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	defer stopVFS(t, first)
	first.Start(firstCtx)
	if _, err := first.WriteAt(firstCtx, "/resume-retry.txt", []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(firstCtx, "/resume-retry.txt"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool {
		pending := first.PendingUploads()
		return len(pending) == 1 && pending[0].RetryCount == 1 && pending[0].NextAttemptAt > time.Now().Add(200*time.Millisecond).UnixNano()
	})
	cancelFirst()

	secondDriver := &countingUploadDriver{}
	second, err := vfs.New(secondDriver, vfs.Options{StorageDir: cacheDir, CacheMaxBytes: 10 << 20, UploadDelay: testUploadDelay})
	if err != nil {
		t.Fatal(err)
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	defer stopVFS(t, second)
	second.Start(secondCtx)
	time.Sleep(100 * time.Millisecond)
	if got := secondDriver.uploadCount(); got != 0 {
		t.Fatalf("resume uploaded before next attempt: count=%d", got)
	}
	waitNoPending(t, second)
	if got := secondDriver.uploadCount(); got != 1 {
		t.Fatalf("resume upload count = %d, want 1", got)
	}
}
