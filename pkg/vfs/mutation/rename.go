package mutation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Resolver resolves virtual paths to entries and parents.
type Resolver interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
}

// PendingRenamer handles the rename of a pending-only upload. IsPending
// probes the source path WITHOUT touching the destination, so the
// coordinator can resolve the source first (preserving error precedence
// and avoiding a useless destination-parent request when the source does
// not exist). RenamePending performs the local pending rename once the
// destination parent is resolved.
type PendingRenamer interface {
	IsPending(path string) bool
	RenamePending(
		ctx context.Context,
		oldPath, newPath string,
		parent drive.Entry,
		name string,
	) error
}

// RenameView is the view surface a rename coordinator commits to.
type RenameView interface {
	InvalidateReadCache(entry drive.Entry)
	CommitRemoteRename(oldPath, newPath string, entry drive.Entry)
}

// Renamer performs the transactional remote rename/move.
type Renamer interface {
	RenameMove(ctx context.Context, entry drive.Entry, dstParentID, newName string) (drive.Entry, error)
}

// Coordinator drives a rename end to end: path normalization, root
// rejection, pending/remote dispatch, source and destination resolution,
// the transactional remote rename, and the view commit (including the
// intermediate-state commit on a PartialError). Capability checks, health
// recording, and adapter construction stay in the VFS shell.
type Coordinator struct {
	resolve Resolver
	pending PendingRenamer
	view    RenameView
	renamer Renamer
}

// NewCoordinator builds a rename coordinator from its narrow dependencies.
func NewCoordinator(resolve Resolver, pending PendingRenamer, view RenameView, renamer Renamer) *Coordinator {
	return &Coordinator{resolve: resolve, pending: pending, view: view, renamer: renamer}
}

// Rename renames or moves oldPath to newPath.
//
// Order of operations preserves error precedence and avoids side effects:
// the source is probed/validated FIRST (pending probe, then remote
// resolve), the destination parent resolves after the source is known to
// exist, and the read cache is invalidated only once both source and
// destination are valid.
func (c *Coordinator) Rename(ctx context.Context, oldPath, newPath string) error {
	oldPath = vfstypes.CleanVirtualPath(oldPath)
	newPath = vfstypes.CleanVirtualPath(newPath)
	if oldPath == "/" || newPath == "/" {
		return fmt.Errorf("vfs: cannot rename root")
	}

	// Pending-only rename: local state, no remote-view commit. The probe
	// never touches the destination.
	if c.pending.IsPending(oldPath) {
		parent, name, err := c.resolve.Parent(ctx, newPath)
		if err != nil {
			return err
		}
		return c.pending.RenamePending(ctx, oldPath, newPath, parent, name)
	}

	entry, err := c.resolve.Resolve(ctx, oldPath)
	if err != nil {
		return err
	}
	parent, name, err := c.resolve.Parent(ctx, newPath)
	if err != nil {
		return err
	}
	c.view.InvalidateReadCache(entry)

	renamed, err := c.renamer.RenameMove(ctx, entry, parent.ID, name)
	if err != nil {
		var partial *PartialError
		if errors.As(err, &partial) {
			// The remote is renamed-but-unmoved: commit that intermediate
			// state (old parent + new name, full entry metadata) so local
			// and remote stay consistent even though the operation failed.
			c.view.CommitRemoteRename(oldPath, vfstypes.JoinVirtualPath(filepath.Dir(oldPath), name), renamed)
		}
		return err
	}
	c.view.CommitRemoteRename(oldPath, newPath, renamed)
	return nil
}
