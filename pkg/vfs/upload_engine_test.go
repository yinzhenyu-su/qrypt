package vfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

func TestUploadEngineExecutesPendingUpload(t *testing.T) {
	ctx := context.Background()
	remote := t.TempDir()
	fs, err := New(localfs.New(remote), Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		UploadDelay: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/engine.txt", []byte("engine upload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/engine.txt"); err != nil {
		t.Fatal(err)
	}
	fs.uploads.Close()
	pending := fs.PendingUploads()
	if len(pending) != 1 {
		t.Fatalf("pending uploads = %d, want 1", len(pending))
	}

	if err := newUploadEngine(fs).Execute(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(remote, "engine.txt")); err != nil || string(data) != "engine upload" {
		t.Fatalf("remote data = %q err=%v", data, err)
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending uploads after engine = %+v, want none", pending)
	}
	history := fs.DebugSnapshot().Mounts[0].HistoricalUploads()
	if len(history) != 1 || history[0].State != string(drive.UploadPhaseCompleted) {
		t.Fatalf("upload history = %+v, want completed", history)
	}
}
