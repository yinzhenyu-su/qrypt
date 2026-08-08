package vfs

import (
	"context"
	"errors"
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

// --- ViewCommitter coordinator behavior (stub-based, no internal maps) ---

// mkdirCoordinatorFS builds a VFS whose mkdir path is driven by injected
// backend + committer stubs, so tests assert coordinator behavior directly
// against the ViewCommitter interface.
type mkdirCoordinatorFixture struct {
	fs        *VFS
	backend   *fakeMutationBackend
	committer *recordingViewCommitter
}

func newMkdirCoordinatorFixture(t *testing.T) *mkdirCoordinatorFixture {
	t.Helper()
	fs := newViewCommitVFS(t) // fake driver with a.txt/b.txt/dir1
	return &mkdirCoordinatorFixture{
		fs:        fs,
		backend:   &fakeMutationBackend{},
		committer: &recordingViewCommitter{},
	}
}

// TestMkdirCommitsExactlyOnceOnRemoteSuccess: a successful remote Mkdir
// commits the (normalized) path exactly once.
func TestMkdirCommitsExactlyOnceOnRemoteSuccess(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	fx.backend.mkdirResult = drive.Entry{ID: "new-id", ParentID: "root", Name: "newdir", IsDir: true}

	entry, err := fx.fs.mkdirWithDeps(context.Background(), "/newdir", fx.backend, fx.committer)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "new-id" {
		t.Fatalf("entry = %+v, want remote result", entry)
	}
	if len(fx.committer.committed) != 1 || fx.committer.committed[0] != "/newdir" {
		t.Fatalf("committed = %+v, want exactly one normalized /newdir", fx.committer.committed)
	}
}

// TestMkdirCommitsNormalizedPath: a trailing-slash path commits the
// normalized form.
func TestMkdirCommitsNormalizedPath(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	fx.backend.mkdirResult = drive.Entry{ID: "new-id", ParentID: "root", Name: "newdir", IsDir: true}

	if _, err := fx.fs.mkdirWithDeps(context.Background(), "/newdir//", fx.backend, fx.committer); err != nil {
		t.Fatal(err)
	}
	if len(fx.committer.committed) != 1 || fx.committer.committed[0] != "/newdir" {
		t.Fatalf("committed = %+v, want normalized /newdir", fx.committer.committed)
	}
}

// TestMkdirNeverCommitsOnRemoteFailure: a failing remote Mkdir must not
// commit anything.
func TestMkdirNeverCommitsOnRemoteFailure(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	fx.backend.mkdirErr = errors.New("remote boom")

	if _, err := fx.fs.mkdirWithDeps(context.Background(), "/newdir", fx.backend, fx.committer); err == nil {
		t.Fatal("want remote mkdir error")
	}
	if len(fx.committer.committed) != 0 {
		t.Fatalf("committed %d times on failure, want 0", len(fx.committer.committed))
	}
}

// TestMkdirCommitsOnceAfterAlreadyExistsRecovery: when the remote reports
// the directory already exists, the coordinator resolves the existing
// directory (caching its siblings) and still commits exactly once.
func TestMkdirCommitsOnceAfterAlreadyExistsRecovery(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	fx.backend.mkdirErr = errors.New("already exists")
	fx.backend.entries = []drive.Entry{
		{ID: "file", ParentID: "root", Name: "file.txt"},
		{ID: "dir", ParentID: "root", Name: "newdir", IsDir: true},
	}

	entry, err := fx.fs.mkdirWithDeps(context.Background(), "/newdir", fx.backend, fx.committer)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.IsDir || entry.ID != "dir" {
		t.Fatalf("entry = %+v, want recovered existing dir", entry)
	}
	if len(fx.committer.committed) != 1 || fx.committer.committed[0] != "/newdir" {
		t.Fatalf("committed = %+v, want exactly one /newdir", fx.committer.committed)
	}
	if fx.committer.cached != 1 {
		t.Fatalf("CacheListedChildren calls = %d, want 1", fx.committer.cached)
	}
}
