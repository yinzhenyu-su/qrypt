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
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

// newRenameCoordinatorFixture drives renameWithDeps with injected backend +
// committer stubs.
type renameCoordinatorFixture struct {
	fs        *VFS
	backend   *fakeMutationBackend
	committer *recordingViewCommitter
}

// addSubDir places a /sub directory in the view so cross-directory rename
// targets resolve.
func (f *renameCoordinatorFixture) addSubDir() {
	newVFSViewCommitter(f.fs).CommitMkdir("/sub", drive.Entry{ID: "sub", Name: "sub", IsDir: true})
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
	committer := newVFSViewCommitter(fx.fs)
	committer.CommitMkdir("/dir", drive.Entry{ID: "dir", Name: "dir", IsDir: true})
	committer.CacheListedChildren("/dir", []drive.Entry{{ID: "sub", Name: "sub", IsDir: true}})

	fx.backend.moveResult = nil
	// Use the real committer: the rebase effect is in the view, not in a
	// recording stub.
	if err := fx.fs.renameWithDeps(context.Background(), "/dir", "/moved", fx.backend, &recordingMutationRuntime{}, newVFSViewCommitter(fx.fs)); err != nil {
		t.Fatal(err)
	}
	// The subtree must be rebased in the view (old child gone, new child
	// present).
	rt := view.NewRuntime(fx.fs.view)
	_, oldSub := rt.CachedEntry("/dir/sub")
	_, newSub := rt.CachedEntry("/moved/sub")
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

// --- rename/move transactional semantics ---

// TestRenameMoveRollbackOnMoveFailure: when the rename lands but the move
// fails and the rollback rename succeeds, the coordinator commits nothing.
func TestRenameMoveRollbackOnMoveFailure(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	// Force a move failure: the source file resolves with a parent that
	// differs from the destination parent (cross-directory rename).
	fx.backend.moveErr = errors.New("move boom")
	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/sub/renamed.txt", fx.backend, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want move error")
	}
	if len(fx.committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times after rollback, want 0", len(fx.committer.renamed))
	}
	// The rollback rename must have run (rename was called twice: forward
	// + rollback).
	if fx.backend.renameCalls != 2 {
		t.Fatalf("rename calls = %d, want 2 (forward + rollback)", fx.backend.renameCalls)
	}
}

// TestRenameMovePartialCommitOnRollbackFailure: when the rename lands, the
// move fails, AND the rollback also fails, the coordinator must commit the
// intermediate remote state (old parent + new name) so local and remote do
// not diverge.
func TestRenameMovePartialCommitOnRollbackFailure(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	orig := *fx.backend
	// Make the backend fail only the second Rename (the rollback) and fail
	// the Move.
	rb := &sequenceBackend{backend: &orig, failRenameOn: 1}
	rb.moveErr = errors.New("move boom")
	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/sub/renamed.txt", rb, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want partial rename/move error")
	}
	if len(fx.committer.renamed) != 1 {
		t.Fatalf("CommitRemoteRename calls = %d, want 1 (intermediate state commit)", len(fx.committer.renamed))
	}
	// The committed intermediate path is the OLD parent + new name: the
	// remote rename landed in the old directory, the move failed, and the
	// rollback failed, so the actual remote state is /renamed.txt.
	if fx.committer.renamed[0][0] != "/a.txt" || fx.committer.renamed[0][1] != "/renamed.txt" {
		t.Fatalf("committed intermediate = %+v, want /a.txt -> /renamed.txt", fx.committer.renamed[0])
	}
}

// sequenceBackend fails the Nth Rename call (0-indexed) and forwards the
// rest to the wrapped backend.
type sequenceBackend struct {
	backend      *fakeMutationBackend
	failRenameOn int
	moveErr      error
}

func (b *sequenceBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.backend.List(ctx, parentID)
}
func (b *sequenceBackend) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	return b.backend.Mkdir(ctx, parentID, name)
}
func (b *sequenceBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	if b.backend.renameCalls == b.failRenameOn {
		return errors.New("rollback boom")
	}
	return b.backend.Rename(ctx, entry, newName)
}
func (b *sequenceBackend) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	if b.moveErr != nil {
		return b.moveErr
	}
	return b.backend.Move(ctx, entry, dstParentID)
}

// TestRenameMoveSameNameCrossDirFailureNoCommit: a same-name cross-directory
// move failure (no rename step) must commit nothing and not roll back.
func TestRenameMoveSameNameCrossDirFailureNoCommit(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	fx.backend.moveErr = errors.New("move boom")
	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/sub/a.txt", fx.backend, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want move error")
	}
	if len(fx.committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times, want 0", len(fx.committer.renamed))
	}
	if fx.backend.renameCalls != 0 {
		t.Fatalf("rename calls = %d, want 0 (same-name move has no rename step)", fx.backend.renameCalls)
	}
}

// TestRenameMoveCrossDirInvalidatesBothParents: a successful cross-directory
// rename invalidates both parent list caches (old and new).
func TestRenameMoveCrossDirInvalidatesBothParents(t *testing.T) {
	fs := newViewCommitVFS(t)
	fx := newRenameCoordinatorFixture(t)
	fx.fs = fs
	fx.addSubDir()
	view := newVFSListingView(fs)

	// Warm list caches for both parents.
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-a", Name: "a.txt", Size: 5}}, time.Now().Add(time.Minute))
	view.CommitRemoteChildren("/sub", []drive.Entry{}, time.Now().Add(time.Minute))
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); !ok {
		t.Fatal("old parent cache not warm")
	}
	if _, ok := view.FreshListCache("/sub", time.Now().Add(5*time.Second)); !ok {
		t.Fatal("new parent cache not warm")
	}

	if err := fs.renameWithDeps(context.Background(), "/a.txt", "/sub/a.txt", &fakeMutationBackend{}, &recordingMutationRuntime{}, newVFSViewCommitter(fs)); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); ok {
		t.Error("old parent list cache not invalidated")
	}
	if _, ok := view.FreshListCache("/sub", time.Now().Add(5*time.Second)); ok {
		t.Error("new parent list cache not invalidated")
	}
}

// TestRenameMoveSameParentInvalidatesOnce: a same-parent rename invalidates
// the single logical parent (the invalidation is idempotent).
func TestRenameMoveSameParentInvalidatesOnce(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)
	view.CommitRemoteChildren("/", []drive.Entry{{ID: "id-a", Name: "a.txt", Size: 5}}, time.Now().Add(time.Minute))
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); !ok {
		t.Fatal("parent cache not warm")
	}
	if err := fs.renameWithDeps(context.Background(), "/a.txt", "/b.txt", &fakeMutationBackend{}, &recordingMutationRuntime{}, newVFSViewCommitter(fs)); err != nil {
		t.Fatal(err)
	}
	if _, ok := view.FreshListCache("/", time.Now().Add(5*time.Second)); ok {
		t.Error("parent list cache not invalidated by same-parent rename")
	}
}

// TestRenameMovePartialPreservesEntryMetadata: the intermediate-state
// commit must carry the full entry - ID, new Name, old ParentID, IsDir,
// Size, ModTime - not a zeroed stub.
func TestRenameMovePartialPreservesEntryMetadata(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	orig := *fx.backend
	rb := &sequenceBackend{backend: &orig, failRenameOn: 1}
	rb.moveErr = errors.New("move boom")

	modTime := time.Unix(987654321, 0)
	// Prime the entry cache so resolve returns full metadata.
	newVFSViewCommitter(fx.fs).CacheListedChildren("/", []drive.Entry{{
		ID: "id-a", ParentID: "parent-a", Name: "a.txt", Size: 42, ModTime: modTime,
	}})

	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/sub/renamed.txt", rb, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want partial rename/move error")
	}
	e := fx.committer.renamedEntry
	if e.ID != "id-a" {
		t.Errorf("committed entry ID = %q, want id-a", e.ID)
	}
	if e.Name != "renamed.txt" {
		t.Errorf("committed entry Name = %q, want new name renamed.txt", e.Name)
	}
	if e.ParentID != "parent-a" {
		t.Errorf("committed entry ParentID = %q, want old parent parent-a", e.ParentID)
	}
	if e.Size != 42 {
		t.Errorf("committed entry Size = %d, want 42", e.Size)
	}
	if !e.ModTime.Equal(modTime) {
		t.Errorf("committed entry ModTime = %v, want %v", e.ModTime, modTime)
	}
}

// TestRenameMovePartialDirKeepsSubtreeHidden: for a directory rename whose
// rollback fails, the intermediate commit must keep IsDir=true so the
// rename overlay hides the old subtree recursively.
func TestRenameMovePartialDirKeepsSubtreeHidden(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	orig := *fx.backend
	rb := &sequenceBackend{backend: &orig, failRenameOn: 1}
	rb.moveErr = errors.New("move boom")

	newVFSViewCommitter(fx.fs).CommitMkdir("/d", drive.Entry{ID: "id-d", ParentID: "parent", Name: "d", IsDir: true, Size: 0})

	if err := fx.fs.renameWithDeps(context.Background(), "/d", "/sub/d2", rb, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want partial rename/move error")
	}
	if !fx.committer.renamedEntry.IsDir {
		t.Error("committed intermediate entry lost IsDir; old subtree will not be hidden recursively")
	}
}

// TestRenameMoveRollbackOnCancelledContext: when the move reports a
// cancellation, the rollback still runs on a detached context and the
// operation reports the move error without committing. (The caller's ctx
// stays live through resolve; the backend simulates a cancelled move.)
func TestRenameMoveRollbackOnCancelledContext(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	fx.backend.moveErr = context.Canceled

	if err := fx.fs.renameWithDeps(context.Background(), "/a.txt", "/sub/renamed.txt", fx.backend, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want error")
	}
	// Rollback ran: the rename ran forward (calls=1) and the detached
	// rollback rename ran again (calls=2); nothing was committed.
	if fx.backend.renameCalls != 2 {
		t.Fatalf("rename calls = %d, want 2 (forward + detached rollback)", fx.backend.renameCalls)
	}
	if len(fx.committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times, want 0", len(fx.committer.renamed))
	}
}

// TestRenameMovePartialDirOverlayHidesSubtree: with the REAL view
// committer, a directory partial commit must make the old path and its
// cached subtree unavailable and surface the new path - not just carry
// IsDir on a stub.
func TestRenameMovePartialDirOverlayHidesSubtree(t *testing.T) {
	fs := newViewCommitVFS(t)
	view := newVFSListingView(fs)
	seed := newVFSViewCommitter(fs)
	seed.CommitMkdir("/d", drive.Entry{ID: "id-d", Name: "d", IsDir: true})
	seed.CacheListedChildren("/d", []drive.Entry{{ID: "id-c", Name: "child.txt"}})
	seed.CommitMkdir("/sub", drive.Entry{ID: "sub", Name: "sub", IsDir: true})

	orig := fakeMutationBackend{}
	rb := &sequenceBackend{backend: &orig, failRenameOn: 1}
	rb.moveErr = errors.New("move boom")

	if err := fs.renameWithDeps(context.Background(), "/d", "/sub/d2", rb, &recordingMutationRuntime{}, newVFSViewCommitter(fs)); err == nil {
		t.Fatal("want partial rename/move error")
	}
	if !view.IsUnavailable("/d") {
		t.Error("old path /d still visible after partial dir commit")
	}
	if !view.IsUnavailable("/d/child.txt") {
		t.Error("old subtree /d/child.txt still visible after partial dir commit")
	}
	if _, ok := view.Entry("/d2"); !ok {
		t.Error("intermediate new path /d2 missing from the view")
	}
}

// cancelOnMoveBackend cancels the caller's context inside Move, then
// records whether the rollback Rename receives a still-live context.
type cancelOnMoveBackend struct {
	backend     *fakeMutationBackend
	cancel      context.CancelFunc
	renameCount int
	rbCtxErr    error
}

func (b *cancelOnMoveBackend) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return b.backend.List(ctx, parentID)
}
func (b *cancelOnMoveBackend) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	return b.backend.Mkdir(ctx, parentID, name)
}
func (b *cancelOnMoveBackend) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	b.renameCount++
	if b.renameCount > 1 {
		// This is the rollback call: capture whether the detached context
		// is still live.
		b.rbCtxErr = ctx.Err()
	}
	return b.backend.Rename(ctx, entry, newName)
}
func (b *cancelOnMoveBackend) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	b.cancel()
	return context.Canceled
}

// TestRenameMoveRollbackGetsLiveContext: the move cancels the caller's
// context; the rollback rename must still receive a live (detached)
// context and the operation must not commit.
func TestRenameMoveRollbackGetsLiveContext(t *testing.T) {
	fx := newRenameCoordinatorFixture(t)
	fx.addSubDir()
	ctx, cancel := context.WithCancel(context.Background())
	backend := &cancelOnMoveBackend{backend: fx.backend, cancel: cancel}

	if err := fx.fs.renameWithDeps(ctx, "/a.txt", "/sub/renamed.txt", backend, &recordingMutationRuntime{}, fx.committer); err == nil {
		t.Fatal("want error")
	}
	if backend.renameCount != 2 {
		t.Fatalf("rename calls = %d, want 2 (forward + rollback)", backend.renameCount)
	}
	if backend.rbCtxErr != nil {
		t.Fatalf("rollback received a cancelled context (%v); WithoutCancel broken", backend.rbCtxErr)
	}
	if len(fx.committer.renamed) != 0 {
		t.Fatalf("CommitRemoteRename called %d times, want 0", len(fx.committer.renamed))
	}
}
