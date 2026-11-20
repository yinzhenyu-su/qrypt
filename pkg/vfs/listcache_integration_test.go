package vfs

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// countingListDriver counts backend directory listings while delegating to a
// real local filesystem driver.
type countingListDriver struct {
	drive.Driver
	lists *atomic.Int64
}

func (d *countingListDriver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if d.lists != nil {
		d.lists.Add(1)
	}
	return d.Driver.List(ctx, parentID)
}

func newListCacheTestVFS(t *testing.T, root, cacheDir, identity string, lists *atomic.Int64) *VFS {
	t.Helper()
	fs, err := New(&countingListDriver{Driver: localfs.New(root), lists: lists}, Options{
		Name:              "m",
		StorageDir:        t.TempDir(),
		ListCacheDir:      cacheDir,
		ListCacheTTL:      time.Minute,
		ListCacheMaxBytes: 1 << 20,
		ListCacheIdentity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestVFSListCacheServesSecondListingAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "m")
	var lists atomic.Int64
	fs := newListCacheTestVFS(t, root, cacheDir, "identity-1", &lists)
	ctx := context.Background()

	first, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("driver lists = %d, want 1", got)
	}
	second, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := lists.Load(); got != 1 {
		t.Fatalf("driver lists after cached list = %d, want 1", got)
	}
	if len(first) != len(second) {
		t.Fatalf("cached listing length = %d, want %d", len(second), len(first))
	}

	// A local mutation drops the persisted listing, so the next list refetches.
	if _, err := fs.Mkdir(ctx, "/newdir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if got := lists.Load(); got != 2 {
		t.Fatalf("driver lists after mutation = %d, want 2", got)
	}

	// A second VFS with the same cache directory (a restart) serves the
	// persisted listing without touching the backend.
	var restartLists atomic.Int64
	fs2 := newListCacheTestVFS(t, root, cacheDir, "identity-1", &restartLists)
	if _, err := fs2.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if got := restartLists.Load(); got != 0 {
		t.Fatalf("restart driver lists = %d, want 0", got)
	}
	// A different identity must not serve the previous account's listing.
	var foreignLists atomic.Int64
	fs3 := newListCacheTestVFS(t, root, cacheDir, "identity-2", &foreignLists)
	if _, err := fs3.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if got := foreignLists.Load(); got != 1 {
		t.Fatalf("foreign identity driver lists = %d, want 1", got)
	}
	for _, f := range []*VFS{fs, fs2, fs3} {
		if err := f.CloseReadCache(); err != nil {
			t.Fatal(err)
		}
	}
}
