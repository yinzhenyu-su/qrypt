package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// TestCoreCloseWaitsForFilesystemWorkers: Core.Close must wait for the
// filesystem's teardown (upload workers, journal/staging writes, read-cache
// flush) to finish before returning. A subsequent filesystem Close with a
// short deadline must return immediately - it would time out if workers
// were still running.
func TestCoreCloseWaitsForFilesystemWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tmp := t.TempDir()
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
	fs.Start(ctx)
	c := newTestCore(t, fs)

	// Warm the upload path: a pending record with staging data exists when
	// Close runs, so the upload worker has real work to tear down.
	if err := fs.Create(ctx, "/note.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/note.txt", []byte("payload"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/note.txt"); err != nil {
		t.Fatal(err)
	}
	// Let the debounce timer fire so a worker actually starts uploading.
	time.Sleep(30 * time.Millisecond)

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("core close: %v", err)
	}
	// After Core.Close, the filesystem teardown must already be complete:
	// Close with a short deadline returns immediately instead of timing out.
	shortCtx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer scancel()
	if err := fs.Close(shortCtx); err != nil {
		t.Fatalf("fs close after core close: %v", err)
	}
}
