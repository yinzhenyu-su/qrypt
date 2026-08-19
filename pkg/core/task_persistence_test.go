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

func TestCorePersistsDeleteBatchTaskHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		DeleteDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}

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
	if item.State != task.StatePartialFailed {
		t.Fatalf("task = %+v, want partial_failed", item)
	}

	reopened := &Core{
		fs:            fs,
		runtimeLayout: RuntimeLayout{StateDir: stateDir},
	}
	got, err := reopened.GetTask(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.StatePartialFailed || len(got.Result.Items) != 2 {
		t.Fatalf("replayed task = %+v", got)
	}

	if err := reopened.DismissTask(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	reopenedAgain := &Core{
		fs:            fs,
		runtimeLayout: RuntimeLayout{StateDir: stateDir},
	}
	if _, err := reopenedAgain.GetTask(ctx, item.ID); err == nil {
		t.Fatal("removed task exists after replay")
	}
}

func TestCorePersistsSingleUploadBatchTaskHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(tmp, "local.txt")
	if err := os.WriteFile(local, []byte("persist upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		UploadDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}

	item, err := c.CreateTask(ctx, task.Request{
		Type:  task.TypeUploadBatch,
		Items: []task.Item{{SourcePath: local, DestPath: "/remote.txt"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || !item.Capabilities.Persistent {
		t.Fatalf("task = %+v, want persistent upload success", item)
	}

	reopened := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	got, err := reopened.GetTask(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != task.TypeUploadBatch || got.State != task.StateSucceeded {
		t.Fatalf("replayed upload task = %+v", got)
	}
}

func TestCoreRecoversInterruptedDirectUploadTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	sourcePath := filepath.Join(tmp, "source.txt")
	payload := []byte("recover direct upload")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		UploadDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	store, err := task.NewPersistentStore(filepath.Join(stateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(task.ManagedTask{Task: task.Task{
		ID:    "direct-recover",
		Type:  task.TypeUploadStreamDirect,
		State: task.StateRunning,
		Path:  "/recovered.txt",
		Name:  "recovered.txt",
		Capabilities: task.Capabilities{
			Cancelable: true,
			Persistent: true,
		},
		Detail: map[string]any{
			"channel": "direct",
			"phase":   "uploading",
			"items": []map[string]any{{
				"item_id":     "item",
				"source_path": sourcePath,
				"dest_path":   "/recovered.txt",
				"name":        "recovered.txt",
				"size":        int64(len(payload)),
			}},
		},
	}})

	recovered := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	t.Cleanup(func() {
		if err := recovered.Close(context.Background()); err != nil {
			t.Logf("core close: %v", err)
		}
	})
	item := waitCoreTask(t, recovered, "direct-recover")
	if item.State != task.StateSucceeded || item.Type != task.TypeUploadStreamDirect {
		t.Fatalf("recovered task = %+v, want succeeded direct task", item)
	}
	if got := drv.uploadedData(); string(got) != string(payload) {
		t.Fatalf("uploaded data = %q, want %q", got, payload)
	}

	reopened := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	got, err := reopened.GetTask(ctx, "direct-recover")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != task.StateSucceeded || got.Error != nil {
		t.Fatalf("replayed recovered task = %+v", got)
	}
}

func TestCoreRecoversCompleteMutableStagingWithoutSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		UploadDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	payload := []byte("complete mutable staging")
	if err := fs.Create(ctx, "/recovered-staging.txt"); err != nil {
		t.Fatal(err)
	}
	if n, err := fs.WriteAt(ctx, "/recovered-staging.txt", payload, 0); err != nil || n != len(payload) {
		t.Fatalf("write staging n=%d err=%v", n, err)
	}
	store, err := task.NewPersistentStore(filepath.Join(stateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(task.ManagedTask{Task: task.Task{
		ID:    "staging-recover",
		Type:  task.TypeUploadStreamBatch,
		State: task.StateFailed,
		Path:  "/recovered-staging.txt",
		Name:  "recovered-staging.txt",
		Capabilities: task.Capabilities{
			Cancelable: true,
			Persistent: true,
		},
		Error: &task.Error{Code: "upload_error", Message: "legacy commit failure", Retryable: true},
		Detail: map[string]any{
			"channel": "staging",
			"phase":   "staging",
			"items": []map[string]any{{
				"item_id":   "item",
				"dest_path": "/recovered-staging.txt",
				"name":      "recovered-staging.txt",
				"size":      int64(len(payload)),
			}},
		},
	}})

	recovered := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	t.Cleanup(func() { _ = recovered.Close(context.Background()) })
	if _, err := recovered.GetTask(ctx, "staging-recover"); err != nil {
		t.Fatal(err)
	}
	var items []task.ItemResult
	deadline := time.Now().Add(5 * time.Second)
	for len(items) == 0 && time.Now().Before(deadline) {
		items, err = recovered.ListTaskItems(ctx, "staging-recover", task.ItemFilter{})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if len(items) != 1 || !items[0].Capabilities.CommitInput || items[0].ResumeOffset != int64(len(payload)) {
		t.Fatalf("recovered items = %+v, want complete staging commit capability", items)
	}
	if err := recovered.CommitStagedUploadItem(ctx, "staging-recover", "item"); err != nil {
		t.Fatal(err)
	}
	finished := waitCoreTask(t, recovered, "staging-recover")
	if finished.State != task.StateSucceeded {
		t.Fatalf("recovered task = %+v, want succeeded", finished)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "recovered-staging.txt")); err != nil || string(data) != string(payload) {
		t.Fatalf("remote data = %q err=%v", data, err)
	}
}

func TestCoreReconcilesCompletedStagingUploadAfterPendingCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	remoteDir := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("already committed remotely")
	if err := os.WriteFile(filepath.Join(remoteDir, "completed.txt"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remoteDir), vfs.Options{StorageDir: filepath.Join(tmp, "cache")})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	store, err := task.NewPersistentStore(filepath.Join(stateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(task.ManagedTask{Task: task.Task{
		ID:    "staging-completed-reconcile",
		Type:  task.TypeUploadStreamBatch,
		State: task.StateRunning,
		Path:  "/completed.txt",
		Name:  "completed.txt",
		Capabilities: task.Capabilities{
			Cancelable: true,
			Persistent: true,
		},
		Detail: map[string]any{
			"channel": "staging",
			"items": []map[string]any{{
				"item_id":   "item",
				"dest_path": "/completed.txt",
				"name":      "completed.txt",
				"size":      int64(len(payload)),
			}},
		},
	}})

	recovered := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	t.Cleanup(func() { _ = recovered.Close(context.Background()) })
	finished := waitCoreTask(t, recovered, "staging-completed-reconcile")
	if finished.State != task.StateSucceeded || finished.Progress.ItemsDone != 1 {
		t.Fatalf("reconciled task = %+v, want succeeded", finished)
	}
	items, err := recovered.ListTaskItems(ctx, finished.ID, task.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != task.StateSucceeded || items[0].CloudBytesDone != int64(len(payload)) {
		t.Fatalf("reconciled items = %+v, want completed remote item", items)
	}
}

func TestCoreRecoversDirectUploadRetryWaitWithSameTaskID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	sourcePath := filepath.Join(tmp, "source.txt")
	payload := []byte("recover retry wait")
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	drv := &directUploadTestDriver{}
	fs, err := vfs.New(drv, vfs.Options{StorageDir: filepath.Join(tmp, "cache"), UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	store, err := task.NewPersistentStore(filepath.Join(stateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(task.ManagedTask{Task: task.Task{
		ID:          "direct-retry-recover",
		Type:        task.TypeUploadStreamDirect,
		State:       task.StateRetryWait,
		RetryCount:  4,
		NextAttempt: time.Now().Add(10 * time.Millisecond),
		Path:        "/recovered.txt",
		Name:        "recovered.txt",
		Capabilities: task.Capabilities{
			Cancelable: true,
			Persistent: true,
		},
		Detail: map[string]any{
			"channel":             "direct",
			"phase":               "retry_wait",
			"auto_retry":          true,
			"auto_upload_item_id": "logical-recover",
			"items": []map[string]any{{
				"item_id":     "logical-recover",
				"source_path": sourcePath,
				"dest_path":   "/recovered.txt",
				"name":        "recovered.txt",
				"size":        int64(len(payload)),
			}},
		},
	}})

	recovered := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	t.Cleanup(func() { _ = recovered.Close(context.Background()) })
	item := waitCoreTask(t, recovered, "direct-retry-recover")
	if item.State != task.StateSucceeded || item.ID != "direct-retry-recover" || item.RetryCount != 4 {
		t.Fatalf("recovered task = %+v, want same id/retry count succeeded", item)
	}
}

func TestCoreRecoversInterruptedDirectUploadTaskWithLocalFSDirect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	stateDir := filepath.Join(tmp, "state")
	sourcePath := filepath.Join(tmp, "source.txt")
	payload := []byte("recover localfs direct upload")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(tmp, "cache"),
		UploadDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	store, err := task.NewPersistentStore(filepath.Join(stateDir, "tasks", "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(task.ManagedTask{Task: task.Task{
		ID:    "direct-fallback-recover",
		Type:  task.TypeUploadStreamDirect,
		State: task.StateRunning,
		Path:  "/fallback-recovered.txt",
		Name:  "fallback-recovered.txt",
		Capabilities: task.Capabilities{
			Cancelable: true,
			Persistent: true,
		},
		Detail: map[string]any{
			"channel": "direct",
			"phase":   "uploading",
			"items": []map[string]any{{
				"item_id":     "item",
				"source_path": sourcePath,
				"dest_path":   "/fallback-recovered.txt",
				"name":        "fallback-recovered.txt",
				"size":        int64(len(payload)),
			}},
		},
	}})

	recovered := &Core{fs: fs, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	t.Cleanup(func() {
		if err := recovered.Close(context.Background()); err != nil {
			t.Logf("core close: %v", err)
		}
	})
	item := waitCoreTask(t, recovered, "direct-fallback-recover")
	if item.State != task.StateSucceeded {
		t.Fatalf("recovered localfs direct task = %+v, want succeeded", item)
	}
	if got := item.Result.Items[0].Phase; got != "direct" {
		t.Fatalf("recovered localfs phase = %q, want direct", got)
	}
	data, err := os.ReadFile(filepath.Join(remote, "fallback-recovered.txt"))
	if err != nil || string(data) != string(payload) {
		t.Fatalf("remote data = %q err=%v, want %q", data, err, payload)
	}
}

func TestCorePersistsCrossMountSingleMoveButNotSameMountMove(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	srcRemote := filepath.Join(tmp, "src")
	dstRemote := filepath.Join(tmp, "dst")
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(srcRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "cross.txt"), []byte("cross"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRemote, "same.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcFS, err := vfs.New(localfs.New(srcRemote), vfs.Options{StorageDir: filepath.Join(tmp, "src-cache"), RootID: srcRemote})
	if err != nil {
		t.Fatal(err)
	}
	dstFS, err := vfs.New(localfs.New(dstRemote), vfs.Options{StorageDir: filepath.Join(tmp, "dst-cache"), RootID: dstRemote})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "src", FS: srcFS}, {Name: "dst", FS: dstFS}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	c := &Core{fs: ns, runtimeLayout: RuntimeLayout{StateDir: stateDir}}

	cross, err := c.CreateTask(ctx, moveTaskRequest("/src/cross.txt", "/dst/cross.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	cross = waitCoreTask(t, c, cross.ID)
	if cross.State != task.StateSucceeded || !cross.Capabilities.Persistent {
		t.Fatalf("cross move task = %+v, want persistent success", cross)
	}

	same, err := c.CreateTask(ctx, moveTaskRequest("/src/same.txt", "/src/same-new.txt", false, false))
	if err != nil {
		t.Fatal(err)
	}
	same = waitCoreTask(t, c, same.ID)
	if same.State != task.StateSucceeded || same.Capabilities.Persistent {
		t.Fatalf("same mount move task = %+v, want memory-only success", same)
	}

	reopened := &Core{fs: ns, runtimeLayout: RuntimeLayout{StateDir: stateDir}}
	if _, err := reopened.GetTask(ctx, cross.ID); err != nil {
		t.Fatalf("cross move task missing after replay: %v", err)
	}
	if _, err := reopened.GetTask(ctx, same.ID); err == nil {
		t.Fatal("same mount move task replayed unexpectedly")
	}
}
