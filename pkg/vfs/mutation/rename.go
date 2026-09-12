package mutation

import (
	"context"
	"errors"
	"fmt"
	pathpkg "path"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Resolver resolves virtual paths to entries and parents.
type Resolver interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
	// RemoteEntry reports the backend object at path independently of any
	// local pending projection. ok is false when the backend holds nothing
	// there, including when the containing directory exists only locally.
	RemoteEntry(ctx context.Context, path string) (drive.Entry, bool, error)
}

// PendingRenamer handles the rename of a pending upload. IsPending probes
// the source path WITHOUT touching the destination, so the coordinator can
// resolve the source first (preserving error precedence and avoiding a
// useless destination-parent request when the source does not exist).
// RenamePending performs the local pending rename once the destination
// parent is resolved; an empty parentID keeps the record's current parent
// (used when a partial remote rename lands at the old parent with a new
// name). RebasePendingUnder moves the pending uploads of a renamed
// directory's descendants onto the renamed subtree.
type PendingRenamer interface {
	IsPending(path string) bool
	RenamePending(
		ctx context.Context,
		oldPath, newPath, parentID, name string,
	) error
	RebasePendingUnder(oldPath, newPath string, entry drive.Entry) error
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

	// Pending source: the local content is newer than the backend's. The
	// backend may still hold an object at the old path - an earlier frozen
	// generation of the same file can finish uploading before the rename, or
	// an existing remote file can be edited in place. That object must move
	// with the rename; otherwise it survives under the old name (a
	// downloader's .qkdownloading temp file, say) while the pending
	// generation uploads to the new one.
	if c.pending.IsPending(oldPath) {
		parent, name, err := c.resolve.Parent(ctx, newPath)
		if err != nil {
			return err
		}
		remoteEntry, hasRemote, err := c.resolve.RemoteEntry(ctx, oldPath)
		if err != nil {
			return err
		}
		if !hasRemote {
			return c.pending.RenamePending(ctx, oldPath, newPath, parent.ID, name)
		}
		return c.renamePendingWithRemote(ctx, oldPath, newPath, parent.ID, name, remoteEntry)
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
			c.view.CommitRemoteRename(oldPath, vfstypes.JoinVirtualPath(pathpkg.Dir(oldPath), name), renamed)
		}
		return err
	}
	c.view.CommitRemoteRename(oldPath, newPath, renamed)
	if renamed.IsDir {
		// Pending uploads written inside the directory before it was renamed
		// follow the subtree; their recorded parent would otherwise point at a
		// directory entry that no longer exists under that name.
		return c.pending.RebasePendingUnder(oldPath, newPath, renamed)
	}
	return nil
}

// renamePendingWithRemote handles a source that has both a backend object and
// a newer pending generation. The backend object moves to the destination
// first - so the old name cannot survive the rename - and the pending record
// is then re-pointed there, so the newer local content replaces the moved
// object when it uploads.
func (c *Coordinator) renamePendingWithRemote(ctx context.Context, oldPath, newPath, parentID, name string, remoteEntry drive.Entry) error {
	c.view.InvalidateReadCache(remoteEntry)
	renamed, err := c.renamer.RenameMove(ctx, remoteEntry, parentID, name)
	if err != nil {
		var partial *PartialError
		if errors.As(err, &partial) {
			// Renamed-but-unmoved: the object landed at the old parent under
			// the new name, so the pending record follows to that same
			// intermediate path and keeps its recorded parent.
			intermediate := vfstypes.JoinVirtualPath(pathpkg.Dir(oldPath), name)
			c.view.CommitRemoteRename(oldPath, intermediate, renamed)
			if moveErr := c.pending.RenamePending(ctx, oldPath, intermediate, "", name); moveErr != nil {
				return errors.Join(err, moveErr)
			}
		}
		return err
	}
	c.view.CommitRemoteRename(oldPath, newPath, renamed)
	return c.pending.RenamePending(ctx, oldPath, newPath, parentID, name)
}
