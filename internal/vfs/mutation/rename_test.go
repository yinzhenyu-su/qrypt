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

	pendingHandled bool
	pendingErr     error
	pendingCalls   int

	invalidated    int
	commits        [][2]string
	committedEntry drive.Entry

	renamerErr   error
	renamerEntry drive.Entry
}

func (f *renameFixture) Resolve(_ context.Context, _ string) (drive.Entry, error) {
	return f.resolveEntry, f.resolveErr
}

func (f *renameFixture) Parent(_ context.Context, _ string) (drive.Entry, string, error) {
	return f.parentEntry, f.parentName, f.parentErr
}

func (f *renameFixture) RenamePending(_ context.Context, _, _ string, _ drive.Entry, _ string) (bool, error) {
	f.pendingCalls++
	return f.pendingHandled, f.pendingErr
}

func (f *renameFixture) InvalidateReadCache(drive.Entry) { f.invalidated++ }

func (f *renameFixture) CommitRemoteRename(oldPath, newPath string, entry drive.Entry) {
	f.commits = append(f.commits, [2]string{oldPath, newPath})
	f.committedEntry = entry
}

func (f *renameFixture) RenameMove(_ context.Context, entry drive.Entry, _, _ string) (drive.Entry, error) {
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
	if fx.pendingCalls != 1 {
		t.Fatalf("pending checks = %d, want 1", fx.pendingCalls)
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
	fx.pendingHandled = true
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
