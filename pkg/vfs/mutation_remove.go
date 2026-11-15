package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/vfs/pathlock"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

func (v *VFS) removeWithRuntime(ctx context.Context, path string, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v.driver, v.testEnabled).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	coordinator := mutation.NewRemoveCoordinator(
		newVFSRenameResolver(v),
		committer,
		newVFSRemoveScheduler(v),
		newVFSRemoveCleanup(v),
	)
	return coordinator.RemoveFile(ctx, path)
}

func (v *VFS) removeDirWithRuntime(ctx context.Context, path string, committer viewCommitter) (err error) {
	v.subtreeMu.Lock()
	defer v.subtreeMu.Unlock()
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v.driver, v.testEnabled).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
		return err
	}
	path = vfstypes.CleanVirtualPath(path)
	entry, err := v.resolve(ctx, path)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return fmt.Errorf("vfs: %s is not a directory", path)
	}
	if err := v.ensureDirectoryEmpty(ctx, path, entry); err != nil {
		return err
	}
	coordinator := mutation.NewRemoveCoordinator(
		newVFSRenameResolver(v),
		committer,
		newVFSRemoveScheduler(v),
		newVFSRemoveCleanup(v),
	)
	return coordinator.RemoveDirectory(ctx, path)
}

// ensureDirectoryEmpty reports ErrDirNotEmpty when path still has visible
// content. visible = backend children not hidden by the delete overlay +
// pending uploads + recently created local directories. Children already
// marked deleted are ignored: their remote delete is coalesced into this
// directory removal. Apple metadata noise is ignored as well.
//
// The backend listing and the overlay lookup cannot be read atomically: a
// child whose delayed delete finishes in between is gone from the backend but
// no longer marked deleted, so the stale listing would make an already empty
// directory look busy (the rmdir after rm race). Confirm a suspect child with
// a fresh listing instead of trusting the stale one. That converges because a
// completed delete drops its overlay marker only after the driver removed the
// object, so the child cannot reappear in a later listing.
func (v *VFS) ensureDirectoryEmpty(ctx context.Context, path string, entry drive.Entry) error {
	path = vfstypes.CleanVirtualPath(path)
	vis := newVFSVisibilityRuntime(v)
	children, err := v.listBackendChildren(ctx, entry.ID)
	if err != nil {
		return err
	}
	for round := 0; ; round++ {
		suspect, ok := firstVisibleChild(path, children, vis)
		if !ok {
			break
		}
		// Bound the confirmation rounds by the observed child count so a
		// concurrently re-created child cannot spin this loop.
		if round >= len(children) {
			return fmt.Errorf("%w: %s (backend child %q)", ErrDirNotEmpty, path, suspect.Name)
		}
		if children, err = v.listBackendChildren(ctx, entry.ID); err != nil {
			return err
		}
		if hasBackendChild(children, suspect.Name) {
			return fmt.Errorf("%w: %s (backend child %q)", ErrDirNotEmpty, path, suspect.Name)
		}
	}
	for _, pending := range v.uploads.PendingUploads() {
		if pending.Path == path || !vfstypes.IsPathUnder(pending.Path, path) {
			continue
		}
		if vis.IsDeleted(pending.Path) {
			continue
		}
		return fmt.Errorf("%w: %s (pending upload %q)", ErrDirNotEmpty, path, pending.Path)
	}
	if child, ok := view.NewRuntime(v.view).RecentLocalChild(path); ok {
		return fmt.Errorf("%w: %s (local dir %q)", ErrDirNotEmpty, path, child)
	}
	return nil
}

// listBackendChildren lists the backend children of a directory, mapping a
// vanished parent (deleted concurrently) to an empty listing.
func (v *VFS) listBackendChildren(ctx context.Context, id string) ([]drive.Entry, error) {
	children, err := v.driver.List(ctx, id)
	if err != nil {
		if errors.Is(err, drive.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return children, nil
}

// firstVisibleChild returns the first backend child that the delete overlay
// does not hide, ignoring Apple metadata noise.
func firstVisibleChild(path string, children []drive.Entry, vis view.Visibility) (drive.Entry, bool) {
	for _, child := range children {
		if view.IsAppleMetadataName(child.Name) {
			continue
		}
		if vis.IsDeleted(vfstypes.JoinVirtualPath(path, child.Name)) {
			continue
		}
		return child, true
	}
	return drive.Entry{}, false
}

// hasBackendChild reports whether name is present in a backend listing.
func hasBackendChild(children []drive.Entry, name string) bool {
	for _, child := range children {
		if child.Name == name {
			return true
		}
	}
	return false
}

// vfsRemoveScheduler adapts the VFS delayed delete to mutation.DeleteScheduler.

type vfsRemoveScheduler struct {
	scheduleDelete func(path string, entry drive.Entry)
}

func newVFSRemoveScheduler(v *VFS) vfsRemoveScheduler {
	return vfsRemoveScheduler{scheduleDelete: v.scheduleDelete}
}

func (r vfsRemoveScheduler) ScheduleDelete(path string, entry drive.Entry) {
	r.scheduleDelete(path, entry)
}

// vfsRemoveCleanup adapts the pending-upload removal surface to
// mutation.RemoveCleanup. It owns the pending-store/hash cleanup directly
// (the former vfsRemoveRuntime is inlined here).

type vfsRemoveCleanup struct {
	locks   *pathlock.State
	uploads *upload.Domain
	viewRT  view.Runtime
	vis     view.Visibility
	deletes vfsDeleteScheduler
	remote  remoteEntryResolver
}

func newVFSRemoveCleanup(v *VFS) vfsRemoveCleanup {
	return vfsRemoveCleanup{
		locks:   v.pathLocks,
		uploads: v.uploads,
		viewRT:  view.NewRuntime(v.view),
		vis:     newVFSVisibilityRuntime(v),
		deletes: newVFSDeleteScheduler(v),
		remote:  v,
	}
}

func (r vfsRemoveCleanup) RemovePendingFile(ctx context.Context, path string) (bool, *drive.Entry, error) {
	path = vfstypes.CleanVirtualPath(path)
	// Take the path lock FIRST and re-check inside it: a pending upload
	// that completes between a bare probe and the lock would otherwise be
	// reported as a pending-only remove while the uploaded remote entry is
	// never committed or scheduled for deletion.
	unlock := r.locks.Lock(path)
	defer unlock()
	if _, ok := r.uploads.PendingByPath(path); !ok {
		return false, nil, nil
	}
	// Resolve the backend object while the pending record still exists: the
	// resolver filters the pending projection by its staging FID, so a match
	// here is the older remote object the pending generation is replacing.
	// A list failure must not silently turn an edited remote file into a
	// pending-only delete: keep the record and surface the error.
	remote, hasRemote, err := r.remote.remoteEntry(ctx, path)
	if err != nil {
		if pending, ok := r.uploads.PendingByPath(path); ok && pending.PermanentFail {
			return true, nil, fmt.Errorf("vfs: cannot remove %q because its permanently failed upload cannot be reconciled: %w; unmount and run `qrypt fs journal prune --failed` to discard the local upload", path, err)
		}
		return true, nil, err
	}
	// Durable store commit FIRST; timer/hash/modtime cleanup runs only
	// after it succeeds, so a failed commit never leaves the path half
	// cleaned.
	if err := r.uploads.RemovePending(path); err != nil {
		return true, nil, err
	}
	// Resolution may have cached the pending projection with its staging ID.
	// Drop it and the parent listing after the durable removal succeeds.
	r.viewRT.DropEntryPath(path)
	r.viewRT.ClearLocalModTime(path)
	if hasRemote {
		return true, &remote, nil
	}
	return true, nil, nil
}

func (r vfsRemoveCleanup) PrepareDirectory(path string) error {
	path = vfstypes.CleanVirtualPath(path)
	// Re-check pending descendants under the same commit that hides the
	// directory. ensureDirectoryEmpty ran before this, but a write accepted in
	// between must not be discarded silently: reject and leave the pending
	// content and timers untouched.
	for _, pending := range r.uploads.PendingUploads() {
		if pending.Path == path || !vfstypes.IsPathUnder(pending.Path, path) {
			continue
		}
		if r.vis.IsDeleted(pending.Path) {
			continue
		}
		return fmt.Errorf("%w: %s (pending upload %q)", ErrDirNotEmpty, path, pending.Path)
	}
	r.uploads.CancelUnder(path)
	r.deletes.TakeoverDirectory(path)
	return nil
}

// Compile-time assertions for the remove adapters.
var _ mutation.RemoveResolver = vfsRenameResolver{}
var _ mutation.RemoveCleanup = vfsRemoveCleanup{}
var _ mutation.DeleteScheduler = vfsRemoveScheduler{}
