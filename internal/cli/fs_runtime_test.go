package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/config"
)

// TestWaitFileSystemIdleNoTimeoutWaitsForSlowUpload: an unbounded idle wait
// (timeout <= 0) keeps waiting until queued uploads land, while a bounded
// wait still reports the pending state.
func TestWaitFileSystemIdleNoTimeoutWaitsForSlowUpload(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{
			ReadCacheDir: filepath.Join(tmp, "cache", "read"),
			UploadDir:    filepath.Join(tmp, "upload"),
			StateDir:     filepath.Join(tmp, "state"),
		},
		Upload: config.UploadConfig{UploadDelay: "500ms"},
		Mounts: []config.MountConfig{{
			Name:   "loc",
			Type:   "localfs",
			Params: config.ParamMap{"root_path": remote},
		}},
	}
	fs, cleanup, err := buildFileSystemFromConfigMount(ctx, cfg, "loc")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	fs.Start(ctx)

	// A bounded wait times out while the upload is still in the delay queue.
	if _, err := fs.WriteAt(ctx, "/f.txt", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	err = waitFileSystemIdle(ctx, fs, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still pending") {
		t.Fatalf("bounded wait err = %v, want still-pending timeout", err)
	}

	// An unbounded wait stays until the queued upload lands.
	if _, err := fs.WriteAt(ctx, "/g.txt", []byte("world"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/g.txt"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := waitFileSystemIdle(ctx, fs, 0); err != nil {
		t.Fatalf("unbounded wait failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Fatalf("unbounded wait returned too early (%v), before the 500ms upload delay", elapsed)
	}
}
