package mutation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// MkdirResolver resolves the target path and its parent.
type MkdirResolver interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
}

// MkdirRemote is the remote-IO surface for creating a directory.
type MkdirRemote interface {
	List(ctx context.Context, parentID string) ([]drive.Entry, error)
	Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error)
}

// MkdirView is the view surface a mkdir commits to: deleted-path
// restoration, the commit, and sibling caching for already-exists
// recovery.
type MkdirView interface {
	RestoreDeleted(path string) (drive.Entry, bool)
	RestoreDeletedAncestor(path string)
	IsUnderRestoredDir(path string) bool
	CommitMkdir(path string, entry drive.Entry)
	CacheListedChildren(parentPath string, entries []drive.Entry)
}

// MkdirCoordinator drives directory creation: existence check, deleted
// path restoration, parent resolution, the remote Mkdir (with
// already-exists recovery), and the view commit. Capability checks,
// health recording, and adapter construction stay in the VFS shell.
type MkdirCoordinator struct {
	resolve MkdirResolver
	remote  MkdirRemote
	view    MkdirView
}

// NewMkdirCoordinator builds a mkdir coordinator from its narrow
// dependencies.
func NewMkdirCoordinator(resolve MkdirResolver, remote MkdirRemote, view MkdirView) *MkdirCoordinator {
	return &MkdirCoordinator{resolve: resolve, remote: remote, view: view}
}

// Mkdir creates path and commits it to the view.
func (c *MkdirCoordinator) Mkdir(ctx context.Context, path string) (drive.Entry, error) {
	path = cleanVirtualPath(path)

	// Already exists (or is a file): report as-is. Only a not-found
	// result proceeds to create; any other resolve error (cancel, auth,
	// network, server) returns unchanged WITHOUT touching the overlay or
	// issuing a write.
	entry, err := c.resolve.Resolve(ctx, path)
	switch {
	case err == nil:
		if entry.IsDir {
			return entry, nil
		}
		return drive.Entry{}, fmt.Errorf("vfs: %s exists and is not a directory", path)
	case !errors.Is(err, drive.ErrNotFound):
		return drive.Entry{}, err
	}

	// Recreate a path that is currently marked deleted.
	if entry, ok := c.view.RestoreDeleted(path); ok {
		return entry, nil
	}
	// Creating inside a deleted ancestor restores that ancestor, and a
	// path under a just-restored directory may already exist remotely.
	c.view.RestoreDeletedAncestor(filepath.Dir(path))
	if c.view.IsUnderRestoredDir(path) {
		entry, err := c.resolve.Resolve(ctx, path)
		switch {
		case err == nil:
			if entry.IsDir {
				return entry, nil
			}
			return drive.Entry{}, fmt.Errorf("vfs: %s exists and is not a directory", path)
		case !errors.Is(err, drive.ErrNotFound):
			return drive.Entry{}, err
		}
		// ErrNotFound: continue creating below.
	}

	parent, name, err := c.resolve.Parent(ctx, path)
	if err != nil {
		return drive.Entry{}, err
	}
	entry, err = c.remote.Mkdir(ctx, parent.ID, name)
	if err != nil {
		if !isAlreadyExistsError(err) {
			return drive.Entry{}, err
		}
		entry, err = c.findExistingChildDir(ctx, filepath.Dir(path), parent.ID, name)
		if err != nil {
			return drive.Entry{}, err
		}
	}
	c.view.CommitMkdir(path, entry)
	return entry, nil
}

// findExistingChildDir recovers an already-existing remote directory by
// listing the parent and caching the siblings.
func (c *MkdirCoordinator) findExistingChildDir(ctx context.Context, parentPath, parentID, name string) (drive.Entry, error) {
	entries, err := c.remote.List(ctx, parentID)
	if err != nil {
		return drive.Entry{}, err
	}
	c.view.CacheListedChildren(parentPath, entries)
	for _, child := range entries {
		if child.Name == name && child.IsDir {
			return child, nil
		}
	}
	return drive.Entry{}, fmt.Errorf("vfs: existing directory not found: %s", joinVirtual(parentPath, name))
}

// isAlreadyExistsError reports whether a remote mkdir error means the
// target already exists, via the drive-level conflict classification.
func isAlreadyExistsError(err error) bool {
	return drive.ErrorCategory(err) == drive.ErrorCategoryConflict
}
