// Package mutation implements the VFS mutation application protocol: the
// transactional remote rename/move with compensation, and (in later
// slices) the per-use-case view commits. It is the coordinator layer for
// local mutations, kept free of VFS internals through narrow interfaces.
package mutation

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// rollbackTimeout bounds the detached rollback rename after a failed move,
// so a cancelled caller context cannot hang the rollback.
const rollbackTimeout = 10 * time.Second

// Remote is the remote-IO surface a transactional rename needs.
type Remote interface {
	Rename(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error)
	Move(ctx context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error)
}

// PartialError reports a remote rename/move that partially applied: the
// name change landed, the parent move failed, and the rollback rename also
// failed. The coordinator must commit the intermediate remote state (old
// parent + new name) to the view so local and remote do not diverge.
type PartialError struct {
	MoveErr     error
	RollbackErr error
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("vfs: rename/move partially applied (move failed: %v; rollback failed: %v)", e.MoveErr, e.RollbackErr)
}

func (e *PartialError) Unwrap() []error {
	return []error{e.MoveErr, e.RollbackErr}
}

// RemoteRenamer performs a remote rename/move as one transactional
// operation: rename first (if the name changes), then move (if the parent
// changes). A move failure after a successful rename triggers a rollback
// rename on a detached context; if the rollback also fails, a PartialError
// is returned so the caller commits the intermediate state.
type RemoteRenamer struct {
	remote Remote
}

// NewRemoteRenamer builds a transaction over a Remote backend.
func NewRemoteRenamer(remote Remote) RemoteRenamer {
	return RemoteRenamer{remote: remote}
}

// RenameMove applies the rename/move transaction. The backend owns the
// resulting identity: each step folds the entry the backend reports into the
// tracked entry, while the caller's requested name and parent stay
// authoritative. On a rollback it returns the original move error. On a
// failed rollback it returns the entry in its intermediate state together
// with a *PartialError.
func (r RemoteRenamer) RenameMove(ctx context.Context, entry drive.Entry, dstParentID, newName string) (drive.Entry, error) {
	oldName := entry.Name
	renamed := false
	if oldName != newName {
		updated, err := r.remote.Rename(ctx, entry, newName)
		if err != nil {
			return drive.Entry{}, err
		}
		entry = mergeRenamedEntry(updated, newName, "")
		renamed = true
	}
	if entry.ParentID != dstParentID {
		updated, err := r.remote.Move(ctx, entry, dstParentID)
		if err != nil {
			if renamed {
				// The move may have failed because ctx was cancelled; the
				// rollback must not inherit that cancellation. The rollback
				// addresses the entry as it stands now - renamed with the
				// identity the rename reported.
				rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
				_, rbErr := r.remote.Rename(rbCtx, entry, oldName)
				cancel()
				if rbErr == nil {
					// Rolled back: the remote is back to its original
					// name and parent, nothing to commit.
					return drive.Entry{}, err
				}
				// Rollback failed: entry already carries the intermediate
				// state - Name is the new name, ParentID is still the old
				// parent - so the coordinator can commit exactly what the
				// remote holds.
				return entry, &PartialError{MoveErr: err, RollbackErr: rbErr}
			}
			return drive.Entry{}, err
		}
		entry = mergeRenamedEntry(updated, "", dstParentID)
	}
	return entry, nil
}

// mergeRenamedEntry takes the entry the backend reported and enforces the
// operation the caller asked for: a backend that keeps provider-assigned ids
// reports the same id, a backend that derives ids from the location reports
// the id of the new location.
func mergeRenamedEntry(reported drive.Entry, newName, dstParentID string) drive.Entry {
	if newName != "" {
		reported.Name = newName
	}
	if dstParentID != "" {
		reported.ParentID = dstParentID
	}
	return reported
}
