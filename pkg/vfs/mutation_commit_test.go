package vfs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestCommitMkdirWritesViewState locks the CommitMkdir state surface: after
// a successful Mkdir the entry cache holds the new directory, the parent's
// list cache is invalidated (next List refetches), and the local-dir marker
// is set. These three writes are the mutation-commit contract this slice
// establishes for future commits (Rename/Remove/UploadCommitted).
func TestCommitMkdirWritesViewState(t *testing.T) {
	fs := newViewCommitVFS(t)
	ctx := context.Background()
	view := newVFSListingView(fs)

	// Warm the parent list cache.
	if _, err := fs.List(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); !ok {
		t.Fatal("parent list cache not warm before mkdir")
	}

	if _, err := fs.Mkdir(ctx, "/newdir"); err != nil {
		t.Fatal(err)
	}

	// 1. Entry cache holds the new directory.
	entry, ok := view.Entry("/newdir")
	if !ok {
		t.Fatal("new dir missing from entry cache after CommitMkdir")
	}
	if !entry.IsDir {
		t.Fatalf("cached entry IsDir = false: %+v", entry)
	}

	// 2. Parent list cache was invalidated.
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); ok {
		t.Error("parent list cache not invalidated by CommitMkdir")
	}

	// 3. Local-dir marker is set (resolve short-circuits to the local view).
	if !fs.isRecentLocalDir("/newdir") {
		t.Error("local-dir marker not set by CommitMkdir")
	}
}

// TestMkdirConcurrentWithList: Mkdir (CommitMkdir) racing concurrent
// listings must be race-free.
func TestMkdirConcurrentWithList(t *testing.T) {
	drv := drive.NewFakeDriver(func(d *drive.FakeDriver) { d.Delay = time.Millisecond })
	if err := drv.Seed(map[string]string{"a.txt": "alpha"}); err != nil {
		t.Fatal(err)
	}
	fs, err := New(drv, Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer func() { _ = fs.Close(context.Background()) }()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				dir := "/d" + string(rune('a'+n)) + ".dir"
				_, _ = fs.Mkdir(ctx, dir)
				_, _ = fs.List(ctx, "/")
			}
		}(i)
	}
	wg.Wait()
}
