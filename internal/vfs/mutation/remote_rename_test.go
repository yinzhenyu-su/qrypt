package mutation

import (
	"context"
	"errors"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// stubRemote records Rename/Move calls and can inject failures per call.
// renameFailAt (1-based) makes the Nth Rename fail; 0 means never.
type stubRemote struct {
	renameErr     error
	renameFailAt  int
	moveErr       error
	renames       int
	moves         int
	lastRenameCtx context.Context
}

func (s *stubRemote) Rename(ctx context.Context, _ drive.Entry, _ string) error {
	s.renames++
	s.lastRenameCtx = ctx
	if s.renameFailAt > 0 && s.renames == s.renameFailAt {
		return s.renameErr
	}
	return nil
}

func (s *stubRemote) Move(context.Context, drive.Entry, string) error {
	s.moves++
	return s.moveErr
}

func newEntry() drive.Entry {
	return drive.Entry{ID: "id-a", ParentID: "parent-a", Name: "a.txt", Size: 7}
}

// TestRenameMoveSuccessChangesNameAndParent: a cross-directory rename with a
// new name issues rename then move and returns the updated entry.
func TestRenameMoveSuccessChangesNameAndParent(t *testing.T) {
	remote := &stubRemote{}
	r := NewRemoteRenamer(remote)
	entry, err := r.RenameMove(context.Background(), newEntry(), "parent-b", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if remote.renames != 1 || remote.moves != 1 {
		t.Fatalf("calls: rename=%d move=%d, want 1/1", remote.renames, remote.moves)
	}
	if entry.Name != "b.txt" || entry.ParentID != "parent-b" {
		t.Fatalf("entry = %+v, want new name + new parent", entry)
	}
}

// TestRenameMoveSameNameSkipsRename: same-name move only calls Move.
func TestRenameMoveSameNameSkipsRename(t *testing.T) {
	remote := &stubRemote{}
	r := NewRemoteRenamer(remote)
	entry, err := r.RenameMove(context.Background(), newEntry(), "parent-b", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if remote.renames != 0 || remote.moves != 1 {
		t.Fatalf("calls: rename=%d move=%d, want 0/1", remote.renames, remote.moves)
	}
	if entry.ParentID != "parent-b" {
		t.Fatalf("entry parent = %q, want parent-b", entry.ParentID)
	}
}

// TestRenameMoveSameParentSkipsMove: same-parent rename only calls Rename.
func TestRenameMoveSameParentSkipsMove(t *testing.T) {
	remote := &stubRemote{}
	r := NewRemoteRenamer(remote)
	entry, err := r.RenameMove(context.Background(), newEntry(), "parent-a", "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if remote.renames != 1 || remote.moves != 0 {
		t.Fatalf("calls: rename=%d move=%d, want 1/0", remote.renames, remote.moves)
	}
	if entry.Name != "b.txt" {
		t.Fatalf("entry name = %q, want b.txt", entry.Name)
	}
}

// TestRenameMoveRenameFailure: a failing rename returns immediately with
// no move and no commit signal.
func TestRenameMoveRenameFailure(t *testing.T) {
	remote := &stubRemote{renameFailAt: 1, renameErr: errors.New("rename boom")}
	r := NewRemoteRenamer(remote)
	if _, err := r.RenameMove(context.Background(), newEntry(), "parent-b", "b.txt"); err == nil {
		t.Fatal("want rename error")
	}
	if remote.moves != 0 {
		t.Fatalf("move calls = %d, want 0", remote.moves)
	}
}

// TestRenameMoveRollbackOnMoveFailure: rename lands, move fails, rollback
// succeeds - returns the move error and leaves the remote at its original
// name (rename called twice).
func TestRenameMoveRollbackOnMoveFailure(t *testing.T) {
	remote := &stubRemote{moveErr: errors.New("move boom")}
	r := NewRemoteRenamer(remote)
	if _, err := r.RenameMove(context.Background(), newEntry(), "parent-b", "b.txt"); err == nil {
		t.Fatal("want move error")
	}
	if remote.renames != 2 {
		t.Fatalf("rename calls = %d, want 2 (forward + rollback)", remote.renames)
	}
}

// TestRenameMovePartialErrorCarriesEntry: when the rollback also fails,
// the returned entry carries the intermediate state (new name, old parent)
// alongside a *PartialError.
func TestRenameMovePartialErrorCarriesEntry(t *testing.T) {
	remote := &stubRemote{renameFailAt: 2, renameErr: errors.New("rollback boom"), moveErr: errors.New("move boom")}
	r := NewRemoteRenamer(remote)
	entry, err := r.RenameMove(context.Background(), newEntry(), "parent-b", "b.txt")
	var partial *PartialError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want *PartialError", err)
	}
	if partial.MoveErr == nil || partial.RollbackErr == nil {
		t.Fatalf("partial = %+v, want both errors set", partial)
	}
	if entry.Name != "b.txt" || entry.ParentID != "parent-a" {
		t.Fatalf("intermediate entry = %+v, want new name + old parent", entry)
	}
	if entry.ID != "id-a" || entry.Size != 7 {
		t.Fatalf("intermediate entry lost metadata: %+v", entry)
	}
}

// TestRenameMoveRollbackUsesLiveContext: the move cancels the caller's
// context; the rollback must still receive a live (detached) context.
func TestRenameMoveRollbackUsesLiveContext(t *testing.T) {
	remote := &stubRemote{moveErr: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	remote = &stubRemote{
		moveErr: context.Canceled,
	}
	r := NewRemoteRenamer(remote)
	// cancel inside move: simulate by wrapping.
	wrapped := &cancelMoveRemote{inner: remote, cancel: cancel}
	r = NewRemoteRenamer(wrapped)
	if _, err := r.RenameMove(ctx, newEntry(), "parent-b", "b.txt"); err == nil {
		t.Fatal("want move error")
	}
	if wrapped.rbCtxErr != nil {
		t.Fatalf("rollback received cancelled context (%v); WithoutCancel broken", wrapped.rbCtxErr)
	}
}

// cancelMoveRemote cancels the caller context in Move and records whether
// the rollback Rename receives a live context.
type cancelMoveRemote struct {
	inner    *stubRemote
	cancel   context.CancelFunc
	rbCtxErr error
}

func (c *cancelMoveRemote) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	c.inner.renames++
	if c.inner.renames > 1 {
		c.rbCtxErr = ctx.Err()
	}
	return c.inner.Rename(ctx, entry, newName)
}

func (c *cancelMoveRemote) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	c.cancel()
	return context.Canceled
}
