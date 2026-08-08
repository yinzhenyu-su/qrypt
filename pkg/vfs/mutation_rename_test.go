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
	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
)

// newRenameCoordinatorFixture drives renameWithDeps with injected backend +
// committer stubs.
type renameCoordinatorFixture struct {
	fs        *VFS
	backend   *fakeMutationBackend
	committer *recordingViewCommitter
}

func newRenameCoordinatorFixture(t *testing.T) *renameCoordinatorFixture {
	t.Helper()
	fs := newViewCommitVFS(t) // fake driver with a.txt/b.txt/dir1
	return &renameCoordinatorFixture{
		fs:        fs,
		backend:   &fakeMutationBackend{},
		committer: &recordingViewCommitter{},
	}
}

// TestRenameNeverCommitsOnRemoteFailure: a failing remote Rename/Move must
// not commit the view.
func TestRenameNeverCommitsOnRemoteFailure(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.backend.renameErr = errors.New("remote boom")
	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/a2.txt", fx.backend, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want remote rename error")
	}
	if len(fx.committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times on failure, want 0", len(fx.committer.renamed))
	}
}

// TestRenameCommitsExactlyOnce: a successful same-parent rename commits
// exactly once with both paths normalized.
func TestRenameCommitsExactlyOnce(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt//", "/a2.txt", fx.backend, &recordingMutationRuntime{}, fx.committer); err != nil {
		t.Fatal(err)
	}
	if len(fx.committer.renamed) != 1 {
		t.Fatalf("CommitRemoteRename calls = %d, want 1", len(fx.committer.renamed))
	}
	got := fx.committer.renamed[0]
	if got[0] != "/a.txt" || got[1] != "/a2.txt" {
		t.Fatalf("renamed = %+v, want normalized /a.txt -> /a2.txt", got)
	}
}

// TestRenameDirCommitsWithSubtree: a directory rename commit rebases cached
// descendants; the committer receives the (directory) entry.
func TestRenameDirCommitsWithSubtree(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	// Warm the cache with a directory + child.
	fx.fs.view.mu.Lock()
	fx.fs.view.entries.Set("/dir", drive.Entry{ID: "dir", Name: "dir", IsDir: true})
	fx.fs.view.entries.Set("/dir/sub", drive.Entry{ID: "sub", Name: "sub", IsDir: true})
	fx.fs.view.mu.Unlock()

	fx.backend.moveResult = nil
	// Use the real committer: the rebase effect is in the view, not in a
	// recording stub.
	if err := fx.fs.renameWithDeps(context.Background(), "/dir", "/moved", fx.backend, &recordingMutationRuntime{}, newVFSViewCommitter(fx.fs)); err != nil {
		t.Fatal(err)
	}
	// The subtree must be rebased in the view (old child gone, new child
	// present).
	fx.fs.view.mu.RLock()
	_, oldSub := fx.fs.view.entries.Get("/dir/sub")
	_, newSub := fx.fs.view.entries.Get("/moved/sub")
	fx.fs.view.mu.RUnlock()
	if oldSub {
		t.Error("old subtree path /dir/sub still cached after dir rename")
	}
	if !newSub {
		t.Error("rebased subtree path /moved/sub missing after dir rename")
	}
}

// TestPendingRenameDoesNotTriggerRemoteCommit: renaming a pending-only path
// updates local pending state and never touches the remote-view commit.
func TestPendingRenameDoesNotTriggerRemoteCommit(t *testing.T) {
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
	// The pending path must not reach the remote-view commit; the local
	// pending rename runs on the real mutation runtime.
	if err := fs.renameWithDeps(ctx, "/draft.txt", "/draft2.txt", &fakeMutationBackend{}, newVFSMutationRuntime(fs), committer); err != nil {
		t.Fatal(err)
	}
	if len(committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times for a pending rename, want 0", len(committer.renamed))
	}
	// The pending record moved.
	if _, ok := fs.uploads.Store().UploadByPath("/draft2.txt"); !ok {
		t.Fatal("pending upload not renamed to /draft2.txt")
	}
}

// TestRenameConcurrentWithListResolve: rename commits racing concurrent
// List/Resolve must be race-free. Uses the real localfs driver (concurrent
// file renames of distinct files are safe there; the fake driver's rekey
// has its own map-iteration quirk under concurrency).
func TestRenameConcurrentWithListResolve(t *testing.T) {
	remote := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(remote, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := New(localfs.New(remote), Options{StorageDir: t.TempDir(), CacheMaxBytes: 10 << 20, UploadDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fs.Start(ctx)
	defer func() { _ = fs.Close(context.Background()) }()

	var wg sync.WaitGroup
	sources := []string{"/a.txt", "/b.txt", "/c.txt"}
	for i, src := range sources {
		wg.Add(1)
		go func(n int, srcPath string) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				newP := "/m" + string(rune('0'+n)) + ".txt"
				_ = fs.Rename(ctx, srcPath, newP)
				_, _ = fs.List(ctx, "/")
				_, _ = fs.resolve(ctx, "/")
				_ = fs.Rename(ctx, newP, srcPath)
			}
		}(i, src)
	}
	wg.Wait()
}
