package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type acknowledgementLossDriver struct {
	*localfs.Driver
	calls     atomic.Int32
	failures  int32
	permanent bool
}

func (d *acknowledgementLossDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	n := d.calls.Add(1)
	if d.permanent {
		return drive.Entry{}, os.ErrPermission
	}
	entry, err := d.Driver.PutSource(ctx, req)
	if err == nil && n <= d.failures {
		return drive.Entry{}, context.DeadlineExceeded
	}
	return entry, err
}

func TestAutoUploadRetryAfterRemoteCommitDoesNotRenameAgain(t *testing.T) {
	oldBase, oldMax := DirectUploadRetryBaseDelay, DirectUploadRetryMaxDelay
	DirectUploadRetryBaseDelay = time.Millisecond
	DirectUploadRetryMaxDelay = time.Millisecond
	t.Cleanup(func() { DirectUploadRetryBaseDelay = oldBase; DirectUploadRetryMaxDelay = oldMax })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	driver := &acknowledgementLossDriver{Driver: localfs.New(remote), failures: 2}
	fs, err := vfs.New(driver, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamDirect, Detail: map[string]any{"auto_retry": true, "auto_retry_limit": 5},
		Items: []task.Item{{ItemID: "one", SourcePath: source, DestPath: "/file.txt", Size: 3}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, c, item.ID)
	if done.State != task.StateSucceeded || done.ID != item.ID || done.Path != "/file (1).txt" || done.RetryCount != 2 {
		t.Fatalf("task %+v", done)
	}
	files, err := os.ReadDir(remote)
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for name, want := range map[string]string{"file.txt": "old", "file (1).txt": "new"} {
		data, err := os.ReadFile(filepath.Join(remote, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s=%q err=%v", name, data, err)
		}
	}
}

func TestAutoUploadRetryBudgetStopsOnSameDestination(t *testing.T) {
	b := &uploadStreamBatch{autoRetry: true, retryLimit: 2}
	if !b.shouldRetryDirectUpload(context.DeadlineExceeded) {
		t.Fatal("first timeout must retry")
	}
	b.retryCount = 2
	if b.shouldRetryDirectUpload(context.DeadlineExceeded) {
		t.Fatal("exhausted retry budget must stop")
	}
	restoredCore := &Core{}
	recovered, err := restoredCore.uploadStreamDirectBatchFromTask(task.Task{ID: "saved", RetryCount: 2, Detail: map[string]any{
		"auto_retry": true, "auto_retry_limit": 2, "items": []map[string]any{{"item_id": "one", "source_path": "token", "dest_path": "/file (1).txt", "size": 3}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.shouldRetryDirectUpload(context.DeadlineExceeded) || recovered.items[0].DestPath != "/file (1).txt" {
		t.Fatal("restart reset budget/path")
	}
}

func TestAutoUploadPermanentErrorNeverRetries(t *testing.T) {
	b := &uploadStreamBatch{autoRetry: true, retryLimit: 5}
	for _, err := range []error{os.ErrPermission, os.ErrNotExist, drive.ErrUnsupported, drive.ErrInvalidInput, context.Canceled} {
		if b.shouldRetryDirectUpload(err) {
			t.Errorf("permanent error retried: %v", err)
		}
	}
	if !b.shouldRetryDirectUpload(errors.New("unclassified transient")) {
		t.Fatal("unknown error should use bounded retry")
	}
}

func TestAutoUploadTerminalFailuresStopWithoutReplacementTasks(t *testing.T) {
	oldBase, oldMax := DirectUploadRetryBaseDelay, DirectUploadRetryMaxDelay
	DirectUploadRetryBaseDelay = time.Millisecond
	DirectUploadRetryMaxDelay = time.Millisecond
	t.Cleanup(func() { DirectUploadRetryBaseDelay = oldBase; DirectUploadRetryMaxDelay = oldMax })
	for _, tc := range []struct {
		name      string
		permanent bool
		calls     int32
		files     int
	}{{"permission", true, 1, 1}, {"budget", false, 3, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			remote := t.TempDir()
			if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			drv := &acknowledgementLossDriver{Driver: localfs.New(remote), failures: 100, permanent: tc.permanent}
			fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
			if err != nil {
				t.Fatal(err)
			}
			defer stopTestVFS(t, fs)
			fs.Start(ctx)
			c := newTestCore(t, fs)
			source := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			created, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamDirect, Detail: map[string]any{"auto_retry": true, "auto_retry_limit": 2},
				Items: []task.Item{{ItemID: "one", SourcePath: source, DestPath: "/file.txt", Size: 3}}, Options: task.Options{ConflictPolicy: "rename"}})
			if err != nil {
				t.Fatal(err)
			}
			done := waitCoreTask(t, c, created.ID)
			if done.State != task.StateFailed || drv.calls.Load() != tc.calls {
				t.Fatalf("task=%+v calls=%d", done, drv.calls.Load())
			}
			files, err := os.ReadDir(remote)
			if err != nil || len(files) != tc.files {
				t.Fatalf("files=%v err=%v", files, err)
			}
			tasks, err := c.ListTasks(ctx, task.Filter{Types: []task.Type{task.TypeUploadStreamDirect}})
			if err != nil || len(tasks) != 1 {
				t.Fatalf("tasks=%v err=%v", tasks, err)
			}
		})
	}
}

func TestAutoUploadRestartRetryKeepsPersistedTaskAndPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	drv := &acknowledgementLossDriver{Driver: localfs.New(remote), failures: 1}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	state := t.TempDir()
	c := newTestCore(t, fs)
	c.runtimeLayout.StateDir = state
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateTask(ctx, task.Request{Type: task.TypeUploadStreamDirect,
		Items: []task.Item{{ItemID: "one", SourcePath: source, DestPath: "/file.txt", Size: 3}}, Options: task.Options{ConflictPolicy: "rename"}})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitCoreTask(t, c, created.ID)
	if failed.State != task.StateFailed {
		t.Fatalf("task=%+v", failed)
	}
	c.tasks.Close()
	restarted := newTestCore(t, fs)
	restarted.runtimeLayout.StateDir = state
	if err := restarted.RetryTask(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	done := waitCoreTask(t, restarted, created.ID)
	if done.State != task.StateSucceeded || done.Path != "/file (1).txt" || done.ID != created.ID {
		t.Fatalf("task=%+v", done)
	}
	files, err := os.ReadDir(remote)
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}
