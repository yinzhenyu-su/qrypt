package mutation

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// RemoveResolver resolves the target path.
type RemoveResolver interface {
	Resolve(ctx context.Context, path string) (drive.Entry, error)
}

// RemoveView is the view surface a remove commits to.
type RemoveView interface {
	CommitRemove(path string, entry drive.Entry)
}

// DeleteScheduler queues the delayed remote delete for a committed remove.
type DeleteScheduler interface {
	ScheduleDelete(path string, entry drive.Entry)
}

// RemoveCleanup handles pending-upload cleanup for removals.
type RemoveCleanup interface {
	// RemovePendingFile removes a pending-only file (local state). It
	// reports handled=true when the path was a pending upload, in which
	// case no remote view commit or remote delete applies.
	RemovePendingFile(path string) (handled bool, err error)
	// PrepareDirectory atomically prepares a directory removal: cancel
	// child uploads, remove pending uploads under the subtree, and cancel
	// child deletes. It must succeed before the directory is hidden or a
	// delete is scheduled.
	PrepareDirectory(path string) error
}

// RemoveCoordinator drives file and directory removal with a strict
// order: normalize -> pending cleanup or remote resolve -> (directory)
// type check -> cleanup success -> CommitRemove -> ScheduleDelete.
// Capability checks, health recording, and adapter construction stay in
// the VFS shell.
type RemoveCoordinator struct {
	resolve  RemoveResolver
	view     RemoveView
	schedule DeleteScheduler
	cleanup  RemoveCleanup
}

// NewRemoveCoordinator builds a remove coordinator from its narrow
// dependencies.
func NewRemoveCoordinator(resolve RemoveResolver, view RemoveView, schedule DeleteScheduler, cleanup RemoveCleanup) *RemoveCoordinator {
	return &RemoveCoordinator{resolve: resolve, view: view, schedule: schedule, cleanup: cleanup}
}

// RemoveFile removes a file: a pending-only path is cleaned locally
// without a remote view commit or a remote delete; an uploaded path is
// resolved, committed, and scheduled.
func (c *RemoveCoordinator) RemoveFile(ctx context.Context, path string) error {
	path = vfstypes.CleanVirtualPath(path)

	handled, err := c.cleanup.RemovePendingFile(path)
	if err != nil || handled {
		return err
	}

	entry, err := c.resolve.Resolve(ctx, path)
	if err != nil {
		return err
	}
	c.view.CommitRemove(path, entry)
	c.schedule.ScheduleDelete(path, entry)
	return nil
}

// RemoveDirectory removes a directory: resolve, type check, directory
// cleanup (which must succeed first), then the view commit and the remote
// delete schedule.
func (c *RemoveCoordinator) RemoveDirectory(ctx context.Context, path string) error {
	path = vfstypes.CleanVirtualPath(path)

	entry, err := c.resolve.Resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	if err := c.cleanup.PrepareDirectory(path); err != nil {
		return err
	}
	c.view.CommitRemove(path, entry)
	c.schedule.ScheduleDelete(path, entry)
	return nil
}
