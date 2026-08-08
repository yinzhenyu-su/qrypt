package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

func TestCreateTaskUploadRemoteUploadsLocalFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remote, "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("task upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, uploadTaskRequest(local, "/remote.txt"))
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeUploadRemote || item.State != task.StateSucceeded || item.Progress.ItemsDone != 1 {
		t.Fatalf("task = %+v, want upload success", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "remote.txt")); err != nil || string(data) != "task upload" {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadRemoteUsesDefaultDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("default upload"), 0o644); err != nil {
		t.Fatal(err)
	}
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
		Type: task.TypeUploadRemote,
		Items: []task.Item{{
			SourcePath: local,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateSucceeded || item.Path != "/cloud/Inbox/local.txt" {
		t.Fatalf("task = %+v, want default upload destination", item)
	}
	if item.Mount != "cloud" {
		t.Fatalf("task mount = %q, want cloud", item.Mount)
	}
	if len(item.Result.Items) != 1 {
		t.Fatalf("result items = %d, want 1", len(item.Result.Items))
	}
	result := item.Result.Items[0]
	if result.DestPath != "/cloud/Inbox/local.txt" || result.Mount != "cloud" || result.RemoteID == "" {
		t.Fatalf("result item = %+v, want remote confirmation", result)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "Inbox", "local.txt")); err != nil || string(data) != "default upload" {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadRemoteBatchPartialFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	local := filepath.Join(t.TempDir(), "ok.txt")
	if err := os.WriteFile(local, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadRemote,
		Items: []task.Item{
			{SourcePath: local, DestPath: "/ok.txt"},
			{SourcePath: filepath.Join(t.TempDir(), "missing.txt"), DestPath: "/missing.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StatePartialFailed || item.Progress.ItemsDone != 2 || item.Progress.ItemsFailed != 1 || item.Error == nil {
		t.Fatalf("task = %+v, want partial failed upload batch", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "ok.txt")); err != nil || string(data) != "ok" {
		t.Fatalf("uploaded ok data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadRemoteConflictPolicySkipExisting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadRemote,
		Items: []task.Item{{
			SourcePath: local,
			DestPath:   "/existing.txt",
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
	if data, err := os.ReadFile(filepath.Join(remote, "existing.txt")); err != nil || string(data) != "remote" {
		t.Fatalf("remote existing data = %q err=%v", data, err)
	}
}

func TestCreateTaskUploadRemoteConflictPolicyFailExisting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	if err := os.WriteFile(filepath.Join(remote, "existing.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateTask(ctx, task.Request{
		Type: task.TypeUploadRemote,
		Items: []task.Item{{
			SourcePath: local,
			DestPath:   "/existing.txt",
		}},
		Options: task.Options{ConflictPolicy: "fail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.State != task.StateFailed || item.Error == nil {
		t.Fatalf("task = %+v, want conflict failure", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "existing.txt")); err != nil || string(data) != "remote" {
		t.Fatalf("remote existing data = %q err=%v", data, err)
	}
}

func TestUploadResultAppliesRemoteUploadTaskMetadata(t *testing.T) {
	result := UploadResult{Entry: drive.Entry{ID: "pending-id", Size: 10}}
	result.applyRemoteTask(task.Task{Detail: map[string]any{
		"result_remote_id": "remote-id",
		"instant":          true,
	}})
	if result.Entry.ID != "remote-id" || !result.Instant {
		t.Fatalf("result = %+v, want remote id and instant metadata", result)
	}
}

func TestVFSUploadBackendReportsNamespaceMount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs, err := vfs.New(localfs.New(t.TempDir()), vfs.Options{Name: "cloud", RootID: "/", StorageDir: filepath.Join(t.TempDir(), "cache")})
	if err != nil {
		t.Fatal(err)
	}
	ns, err := vfs.NewNamespace([]vfs.Mount{{Name: "cloud", FS: fs}})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, ns)
	ns.Start(ctx)
	backend := NewVFSUploadBackend(ns)
	if got := backend.MountForPath("/cloud/a.txt"); got != "cloud" {
		t.Fatalf("MountForPath = %q, want cloud", got)
	}
}

func uploadTaskRequest(localPath, remotePath string) task.Request {
	return task.Request{
		Type: task.TypeUploadRemote,
		Items: []task.Item{{
			SourcePath: localPath,
			DestPath:   remotePath,
		}},
	}
}
