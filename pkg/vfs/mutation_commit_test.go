package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// --- ViewCommitter CommitRemove coordinator behavior ---

// TestRemoveCommitsExactlyOnce: a successful local remove commits the
// (normalized) path exactly once through ViewCommitter.
func TestRemoveCommitsExactlyOnce(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	if err := fx.fs.removeWithRuntime(context.Background(), "/b.txt", newVFSRemoveRuntime(fx.fs), fx.committer); err != nil {
		t.Fatal(err)
	}
	if len(fx.committer.removed) != 1 || fx.committer.removed[0] != "/b.txt" {
		t.Fatalf("CommitRemove = %+v, want exactly one /b.txt", fx.committer.removed)
	}
}

// TestRemoveCommitsNormalizedPath: a trailing-slash path commits the
// normalized form.
func TestRemoveCommitsNormalizedPath(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	if err := fx.fs.removeWithRuntime(context.Background(), "/b.txt//", newVFSRemoveRuntime(fx.fs), fx.committer); err != nil {
		t.Fatal(err)
	}
	if len(fx.committer.removed) != 1 || fx.committer.removed[0] != "/b.txt" {
		t.Fatalf("CommitRemove = %+v, want normalized /b.txt", fx.committer.removed)
	}
}

// TestRemoveNeverCommitsOnResolveFailure: an unresolvable path must not
// commit anything.
func TestRemoveNeverCommitsOnResolveFailure(t *testing.T) {
	fx := newMkdirCoordinatorFixture(t)
	if err := fx.fs.removeWithRuntime(context.Background(), "/missing.txt", newVFSRemoveRuntime(fx.fs), fx.committer); err == nil {
		t.Fatal("want resolve error")
	}
	if len(fx.committer.removed) != 0 {
		t.Fatalf("CommitRemove called %d times on resolve failure, want 0", len(fx.committer.removed))
	}
}

// TestRemovePendingDoesNotCommitView: removing a path that only exists as a
// pending upload goes through the pending-removal path and never touches
// the view committer.
func TestRemovePendingDoesNotCommitView(t *testing.T) {
	fs := newPendingViewVFS(t)
	ctx := context.Background()
	if err := fs.Create(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteAt(ctx, "/draft.txt", []byte("d"), 0); err != nil {
		t.Fatal(err)
	}
	if err := fs.Flush(ctx, "/draft.txt"); err != nil {
		t.Fatal(err)
	}
	committer := &recordingViewCommitter{}
	if err := fs.removeWithRuntime(ctx, "/draft.txt", newVFSRemoveRuntime(fs), committer); err != nil {
		t.Fatal(err)
	}
	if len(committer.removed) != 0 {
		t.Fatalf("CommitRemove called %d times for a pending upload, want 0", len(committer.removed))
	}
	if pending := fs.PendingUploads(); len(pending) != 0 {
		t.Fatalf("pending = %+v, want none after removal", pending)
	}
}

// TestCommitRemoveWritesViewState locks the CommitRemove state surface:
// the path becomes unavailable (overlay), the entry cache drops it, and
// the read cache is invalidated.
func TestCommitRemoveWritesViewState(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)
	// Warm the entry cache for b.txt.
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-b", Name: "b.txt", Size: 4}}, time.Now().Add(time.Minute))
	if _, ok := view.Entry("/b.txt"); !ok {
		t.Fatal("b.txt not cached before remove")
	}
	// Warm the read cache for b.txt. The entry must match the remove
	// commit exactly (same ID + ModTime) so the cache key lines up.
	modTime := time.Now()
	entry := drive.Entry{ID: "id-b", Name: "b.txt", Size: 4, ModTime: modTime}
	staging := filepath.Join(t.TempDir(), "b.staging")
	if err := os.WriteFile(staging, []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs.seedReadCacheFromStaging(entry, staging)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	if before := fs.DebugSnapshot().Mounts[0].ReadCacheState(); before.Bytes == 0 {
		t.Fatal("read cache not seeded before remove")
	}

	newVFSViewCommitter(fs).CommitRemove("/b.txt", entry)

	if !view.IsUnavailable("/b.txt") {
		t.Error("removed path still available after CommitRemove")
	}
	if _, ok := view.Entry("/b.txt"); ok {
		t.Error("removed path still in entry cache after CommitRemove")
	}
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	if after := fs.DebugSnapshot().Mounts[0].ReadCacheState(); after.Bytes != 0 {
		t.Errorf("read cache not invalidated by CommitRemove: %+v", after)
	}
}

// --- ViewCommitter CommitUploadedEntry coordinator behavior ---

// TestCommitUploadedEntryWritesViewState: a completed upload commit writes
// the entry, unhides the copy child, invalidates the parent list cache, and
// seeds the read cache from staging when a staging path is provided.
func TestCommitUploadedEntryWritesViewState(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)
	fs.Start(context.Background())
	t.Cleanup(func() { _ = fs.Close(context.Background()) })

	// Seed a staging file.
	staging := filepath.Join(t.TempDir(), "up.staging")
	if err := os.WriteFile(staging, []byte("uploaded"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Warm the parent list cache so invalidation is observable.
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-r", Name: "remote.txt", Size: 6}}, time.Now().Add(time.Minute))
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); !ok {
		t.Fatal("parent list cache not warm before commit")
	}

	entry := drive.Entry{ID: "up-id", Name: "up.txt", Size: 8, ModTime: time.Now()}
	newVFSViewCommitter(fs).CommitUploadedEntry("/up.txt", entry, staging)
	if err := fs.FlushReadCache(); err != nil {
		t.Fatal(err)
	}

	if got, ok := view.Entry("/up.txt"); !ok || got.ID != "up-id" {
		t.Fatalf("uploaded entry = %+v, ok=%v", got, ok)
	}
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); ok {
		t.Error("parent list cache not invalidated by CommitUploadedEntry")
	}
	if cache := fs.DebugSnapshot().Mounts[0].ReadCacheState(); cache.Bytes == 0 {
		t.Error("read cache not seeded by CommitUploadedEntry")
	}
}
