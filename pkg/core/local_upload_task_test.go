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

func TestCreateLocalUploadTaskWaitsStableAndUploads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := t.TempDir()
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("local upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, err := vfs.New(localfs.New(remote), vfs.Options{StorageDir: filepath.Join(t.TempDir(), "cache"), UploadDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestVFS(t, fs)
	fs.Start(ctx)
	c := newTestCore(t, fs)

	item, err := c.CreateLocalUploadTask(ctx, LocalUploadTaskRequest{
		Items: []LocalUploadTaskItem{{
			LocalPath: local,
			DestPath:  "/uploaded.txt",
		}},
		WaitStable: true,
		Stability:  LocalFileStabilityOptions{QuietMS: 5, PollMS: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	item = waitCoreTask(t, c, item.ID)
	if item.Type != task.TypeUploadRemote || item.State != task.StateSucceeded {
		t.Fatalf("task = %+v, want local upload success", item)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "uploaded.txt")); err != nil || string(data) != "local upload" {
		t.Fatalf("remote upload data = %q err=%v", data, err)
	}
}

func TestCreateLocalUploadTaskRequiresLocalPath(t *testing.T) {
	c := &Core{}
	_, err := c.CreateLocalUploadTask(context.Background(), LocalUploadTaskRequest{
		Items: []LocalUploadTaskItem{{DestPath: "/missing.txt"}},
	})
	if err == nil {
		t.Fatal("CreateLocalUploadTask err = nil, want local_path error")
	}
}
