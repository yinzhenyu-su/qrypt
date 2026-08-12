package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/config"
)

// TestWaitFileSystemIdleNoTimeoutWaitsForSlowUpload: an unbounded idle wait
// (timeout <= 0) keeps waiting until queued uploads land, while a bounded
// wait still reports the pending state.
func TestWaitFileSystemIdleNoTimeoutWaitsForSlowUpload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

// TestCLICleanupWaitsForJournalWrites: the CLI cleanup order (close the
// filesystem and wait for its workers, then release external resources) must
// not race still-running journal/staging writes. After cleanup the upload
// directory must be quiescent: no half-written journal temp files, and the
// directory is fully removable.
func TestCLICleanupWaitsForJournalWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	uploadDir := filepath.Join(tmp, "upload")
	cfg := &config.Config{
		Storage: config.StorageConfig{
			ReadCacheDir: filepath.Join(tmp, "cache", "read"),
			UploadDir:    uploadDir,
			StateDir:     filepath.Join(tmp, "state"),
		},
		Upload: config.UploadConfig{UploadDelay: "10ms"},
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
	fs.Start(ctx)

	// Warm the journal/staging path and let an upload worker start, so Close
	// has real in-flight work to wait for.
	if err := fs.Create(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/f.txt", []byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/f.txt"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)

	// Cleanup in the CLI dependency order: close + wait, then external
	// cleanup. If Close raced the worker, a .tmp journal file could be left
	// behind mid-rename.
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("fs close: %v", err)
	}
	cleanup()

	// No half-written journal temp files may remain, and the upload
	// directory must be fully removable.
	var tmps []string
	_ = filepath.WalkDir(uploadDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".tmp") {
			tmps = append(tmps, path)
		}
		return nil
	})
	if len(tmps) > 0 {
		t.Fatalf("journal temp files left after cleanup: %v", tmps)
	}
	if err := os.RemoveAll(uploadDir); err != nil {
		t.Fatalf("remove upload dir after cleanup: %v", err)
	}
}
