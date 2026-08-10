package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// renameFixture provides stub Resolver/PendingRenamer/RenameView/Renamer.
type renameFixture struct {
	resolveEntry drive.Entry
	resolveErr   error
	parentEntry  drive.Entry
	parentName   string
	parentErr    error

	pending      bool
	pendingErr   error
	pendingCalls int

	invalidated    int
	commits        [][2]string
	committedEntry drive.Entry

	renamerErr   error
	renamerEntry drive.Entry

	// order records each coordinator call for precedence assertions.
	order []string
}

func (f *renameFixture) Resolve(_ context.Context, _ string) (drive.Entry, error) {
	f.order = append(f.order, "resolve")
	return f.resolveEntry, f.resolveErr
}

func (f *renameFixture) Parent(_ context.Context, _ string) (drive.Entry, string, error) {
	f.order = append(f.order, "parent")
	return f.parentEntry, f.parentName, f.parentErr
}

func (f *renameFixture) IsPending(string) bool {
	f.order = append(f.order, "is_pending")
	return f.pending
}

func (f *renameFixture) RenamePending(_ context.Context, _, _ string, _ drive.Entry, _ string) error {
	f.pendingCalls++
	f.order = append(f.order, "rename_pending")
	return f.pendingErr
}

func (f *renameFixture) InvalidateReadCache(drive.Entry) {
	f.invalidated++
	f.order = append(f.order, "invalidate")
}

func (f *renameFixture) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	f.commits = append(f.commits, [2]string{oldPath, newPath})
	f.committedEntry = entry
	f.order = append(f.order, "commit")
}

func (f *renameFixture) RenameMove(_ context.Context, entry drive.Entry, _, _ string) (drive.Entry, error) {
	f.order = append(f.order, "renamemove")
	if f.renamerErr != nil {
		// Mirror the real RemoteRenamer: on a PartialError the entry is
		// returned in its intermediate state (new name, old parent).
		entry.Name = f.renamerEntry.Name
		return entry, f.renamerErr
	}
	entry.Name = f.renamerEntry.Name
	entry.ParentID = f.renamerEntry.ParentID
	return entry, nil
}

func newRenameFixture() *renameFixture {
	return &renameFixture{
		resolveEntry: drive.Entry{ID: "id-a", ParentID: "parent-a", Name: "a.txt", Size: 7},
		parentEntry:  drive.Entry{ID: "parent-b"},
		parentName:   "b.txt",
		renamerEntry: drive.Entry{ID: "id-a", ParentID: "parent-b", Name: "b.txt", Size: 7},
	}
}

func (f *renameFixture) coordinator() *Coordinator {
	return NewCoordinator(f, f, f, f)
}

// TestCoordinatorRenameCommitsExactlyOnce: a successful remote rename
// resolves source, invalidates the read cache, and commits exactly once
// with normalized paths.
func TestCoordinatorRenameCommitsExactlyOnce(t *testing.T) {
	fx := newRenameFixture()
	if err := fx.coordinator().Rename(context.Background(), "/a.txt//", "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if len(fx.commits) != 1 {
		t.Fatalf("commits = %+v, want exactly 1", fx.commits)
	}
	if fx.commits[0][0] != "/a.txt" || fx.commits[0][1] != "/b.txt" {
		t.Fatalf("commit paths = %+v, want normalized /a.txt -> /b.txt", fx.commits[0])
	}
	if fx.invalidated != 1 {
		t.Fatalf("read-cache invalidations = %d, want 1", fx.invalidated)
	}
	if len(fx.order) == 0 || fx.order[0] != "is_pending" {
		t.Fatalf("first call = %v, want pending probe first", fx.order)
	}
	if fx.pendingCalls != 0 {
		t.Fatalf("pending renames = %d, want 0 (probe only)", fx.pendingCalls)
	}
}

// TestCoordinatorRenameZeroCommitOnFailure: a plain rename failure commits
// nothing.
func TestCoordinatorRenameZeroCommitOnFailure(t *testing.T) {
	fx := newRenameFixture()
	fx.renamerErr = errors.New("remote boom")
	if err := fx.coordinator().Rename(context.Background(), "/a.txt", "/b.txt"); err == nil {
		t.Fatal("want remote error")
	}
	if len(fx.commits) != 0 {
		t.Fatalf("commits = %+v, want 0", fx.commits)
	}
}

// TestCoordinatorRenamePartialCommitsIntermediate: a PartialError commits
// the intermediate state (old parent + new name) exactly once.
func TestCoordinatorRenamePartialCommitsIntermediate(t *testing.T) {
	fx := newRenameFixture()
	fx.renamerErr = &PartialError{MoveErr: errors.New("move"), RollbackErr: errors.New("rollback")}
	fx.renamerEntry = drive.Entry{ID: "id-a", ParentID: "parent-a", Name: "b.txt", Size: 7}
	if err := fx.coordinator().Rename(context.Background(), "/a.txt", "/b.txt"); err == nil {
		t.Fatal("want partial error")
	}
	if len(fx.commits) != 1 {
		t.Fatalf("commits = %+v, want 1 (intermediate)", fx.commits)
	}
	if fx.commits[0][0] != "/a.txt" || fx.commits[0][1] != "/b.txt" {
		t.Fatalf("intermediate commit = %+v, want /a.txt -> /b.txt", fx.commits[0])
	}
	// The committed entry keeps the new name and old parent.
	if fx.committedEntry.Name != "b.txt" || fx.committedEntry.ParentID != "parent-a" {
		t.Fatalf("committed entry = %+v, want new name + old parent", fx.committedEntry)
	}
}

// TestCoordinatorRenamePendingDispatch: a pending-only rename goes through
// RenamePending and never touches the renamer or view commit.
func TestCoordinatorRenamePendingDispatch(t *testing.T) {
	fx := newRenameFixture()
	fx.pending = true
	if err := fx.coordinator().Rename(context.Background(), "/draft.txt", "/draft2.txt"); err != nil {
		t.Fatal(err)
	}
	if len(fx.commits) != 0 {
		t.Fatalf("commits = %+v, want 0 for pending rename", fx.commits)
	}
	if fx.pendingCalls != 1 {
		t.Fatalf("pending calls = %d, want 1", fx.pendingCalls)
	}
}

// TestCoordinatorRenameRejectsRoot: renaming to/from root fails.
func TestCoordinatorRenameRejectsRoot(t *testing.T) {
	fx := newRenameFixture()
	if err := fx.coordinator().Rename(context.Background(), "/", "/x"); err == nil {
		t.Fatal("want root rejection")
	}
	if err := fx.coordinator().Rename(context.Background(), "/x", "/"); err == nil {
		t.Fatal("want root rejection")
	}
	if len(fx.commits) != 0 {
		t.Fatalf("commits = %+v, want 0", fx.commits)
	}
}

// TestCoordinatorRenameSourceMissingSkipsDestination: when the source does
// not exist, the destination parent is never resolved (no useless remote
// request, error precedence preserved).
func TestCoordinatorRenameSourceMissingSkipsDestination(t *testing.T) {
	fx := newRenameFixture()
	fx.resolveErr = errors.New("source not found")
	if err := fx.coordinator().Rename(context.Background(), "/missing.txt", "/b.txt"); err == nil {
		t.Fatal("want source error")
	}
	parents := 0
	for _, step := range fx.order {
		if step == "parent" {
			parents++
		}
	}
	if parents != 0 {
		t.Fatalf("parent calls = %d, want 0 (source missing)", parents)
	}
	if fx.invalidated != 0 || len(fx.commits) != 0 {
		t.Fatalf("side effects on missing source: invalidated=%d commits=%d", fx.invalidated, len(fx.commits))
	}
}

// TestCoordinatorRenameDestinationFailureNoSideEffects: when the source
// resolves but the destination parent fails, the renamer is not called,
// nothing commits, and the read cache is not invalidated.
func TestCoordinatorRenameDestinationFailureNoSideEffects(t *testing.T) {
	fx := newRenameFixture()
	fx.parentErr = errors.New("dest parent not found")
	if err := fx.coordinator().Rename(context.Background(), "/a.txt", "/missing-dir/b.txt"); err == nil {
		t.Fatal("want destination error")
	}
	for _, step := range fx.order {
		switch step {
		case "renamemove", "invalidate", "commit":
			t.Fatalf("unexpected %q on destination failure", step)
		}
	}
	if len(fx.commits) != 0 || fx.invalidated != 0 {
		t.Fatalf("side effects on destination failure: invalidated=%d commits=%d", fx.invalidated, len(fx.commits))
	}
}

// TestCoordinatorRenamePendingSkipsSourceResolve: a pending source never
// triggers a remote source resolve.
func TestCoordinatorRenamePendingSkipsSourceResolve(t *testing.T) {
	fx := newRenameFixture()
	fx.pending = true
	if err := fx.coordinator().Rename(context.Background(), "/draft.txt", "/draft2.txt"); err != nil {
		t.Fatal(err)
	}
	for _, step := range fx.order {
		if step == "resolve" {
			t.Fatal("source resolve called for a pending rename")
		}
	}
	if fx.pendingCalls != 1 {
		t.Fatalf("pending renames = %d, want 1", fx.pendingCalls)
	}
}

// TestCoordinatorRenamePendingDestinationFailureSkipsRename: a pending
// source with a failing destination parent does not execute the pending
// rename.
func TestCoordinatorRenamePendingDestinationFailureSkipsRename(t *testing.T) {
	fx := newRenameFixture()
	fx.pending = true
	fx.parentErr = errors.New("dest parent not found")
	if err := fx.coordinator().Rename(context.Background(), "/draft.txt", "/missing/draft2.txt"); err == nil {
		t.Fatal("want destination error")
	}
	if fx.pendingCalls != 0 {
		t.Fatalf("pending renames executed = %d, want 0", fx.pendingCalls)
	}
}

// TestCoordinatorRenameSuccessOrder: the success path runs in the order
// probe -> resolve source -> parent -> invalidate -> renamemove -> commit.
func TestCoordinatorRenameSuccessOrder(t *testing.T) {
	fx := newRenameFixture()
	if err := fx.coordinator().Rename(context.Background(), "/a.txt", "/b.txt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"is_pending", "resolve", "parent", "invalidate", "renamemove", "commit"}
	if len(fx.order) != len(want) {
		t.Fatalf("order = %v, want %v", fx.order, want)
	}
	for i := range want {
		if fx.order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q (full order %v)", i, fx.order[i], want[i], fx.order)
		}
	}
}
