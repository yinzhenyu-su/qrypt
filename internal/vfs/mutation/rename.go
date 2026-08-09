package mutation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// CleanVirtualPath normalizes qrypt virtual paths to absolute slash paths.
func CleanVirtualPath(path string) string {
	return vfstypes.CleanVirtualPath(path)
}

// joinVirtual joins a parent virtual path and a child name.
func joinVirtual(parent, name string) string {
	parent = CleanVirtualPath(parent)
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

// Resolver resolves virtual paths to entries and parents.
type Resolver interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
}

// PendingRenamer handles the rename of a pending-only upload. It reports
// whether the path was a pending upload (handled) - when false, the caller
// proceeds with the remote rename path.
type PendingRenamer interface {
	RenamePending(
		ctx context.Context,
		oldPath, newPath string,
		parent drive.Entry,
		name string,
	) (handled bool, err error)
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
func (c *Coordinator) Rename(ctx context.Context, oldPath, newPath string) error {
	oldPath = CleanVirtualPath(oldPath)
	newPath = CleanVirtualPath(newPath)
	if oldPath == "/" || newPath == "/" {
		return fmt.Errorf("vfs: cannot rename root")
	}

	// Destination parent resolves once and feeds both paths.
	parent, name, err := c.resolve.Parent(ctx, newPath)
	if err != nil {
		return err
	}

	// Pending-only rename: local state, no remote-view commit.
	handled, err := c.pending.RenamePending(ctx, oldPath, newPath, parent, name)
	if err != nil || handled {
		return err
	}

	entry, err := c.resolve.Resolve(ctx, oldPath)
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
			c.view.CommitRemoteRename(oldPath, joinVirtual(filepath.Dir(oldPath), name), renamed)
		}
		return err
	}
	c.view.CommitRemoteRename(oldPath, newPath, renamed)
	return nil
}
