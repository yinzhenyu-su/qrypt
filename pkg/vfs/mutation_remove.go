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
func (v *VFS) ensureDirectoryEmpty(ctx context.Context, path string, entry drive.Entry) error {
	path = vfstypes.CleanVirtualPath(path)
	vis := newVFSVisibilityRuntime(v)
	children, err := v.driver.List(ctx, entry.ID)
	if err != nil {
		if errors.Is(err, drive.ErrNotFound) {
			children = nil
		} else {
			return err
		}
	}
	for _, child := range children {
		if view.IsAppleMetadataName(child.Name) {
			continue
		}
		childPath := vfstypes.JoinVirtualPath(path, child.Name)
		if vis.IsDeleted(childPath) {
			continue
		}
		return fmt.Errorf("%w: %s", ErrDirNotEmpty, path)
	}
	for _, pending := range v.uploads.Store().PendingUploads() {
		if pending.Path == path || !vfstypes.IsPathUnder(pending.Path, path) {
			continue
		}
		if vis.IsDeleted(pending.Path) {
			continue
		}
		return fmt.Errorf("%w: %s", ErrDirNotEmpty, path)
	}
	if view.NewRuntime(v.view).HasRecentLocalChild(path) {
		return fmt.Errorf("%w: %s", ErrDirNotEmpty, path)
	}
	return nil
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
	store   *uploadStore
	uploads *uploadService
	viewRT  view.Runtime
	vis     view.Visibility
	hashes  *upload.HashTracker
	deletes vfsDeleteScheduler
	remote  remoteEntryResolver
}

func newVFSRemoveCleanup(v *VFS) vfsRemoveCleanup {
	return vfsRemoveCleanup{
		locks:   v.pathLocks,
		store:   v.uploads.Store(),
		uploads: v.uploads,
		viewRT:  view.NewRuntime(v.view),
		vis:     newVFSVisibilityRuntime(v),
		hashes:  v.hashes,
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
	if _, ok := r.store.UploadByPath(path); !ok {
		return false, nil, nil
	}
	// Resolve the backend object while the pending record still exists: the
	// resolver filters the pending projection by its staging FID, so a match
	// here is the older remote object the pending generation is replacing.
	// A list failure must not silently turn an edited remote file into a
	// pending-only delete: keep the record and surface the error.
	remote, hasRemote, err := r.remote.remoteEntry(ctx, path)
	if err != nil {
		return true, nil, err
	}
	// Durable store commit FIRST; timer/hash/modtime cleanup runs only
	// after it succeeds, so a failed commit never leaves the path half
	// cleaned.
	if err := r.store.RemoveUpload(path); err != nil {
		return true, nil, err
	}
	r.uploads.CancelUpload(path)
	// Resolution may have cached the pending projection with its staging ID.
	// Drop it and the parent listing after the durable removal succeeds.
	r.viewRT.DropEntryPath(path)
	r.viewRT.ClearLocalModTime(path)
	r.hashes.RemovePath(path)
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
	for _, pending := range r.store.PendingUploads() {
		if pending.Path == path || !vfstypes.IsPathUnder(pending.Path, path) {
			continue
		}
		if r.vis.IsDeleted(pending.Path) {
			continue
		}
		return fmt.Errorf("%w: %s", ErrDirNotEmpty, path)
	}
	r.hashes.RemoveUnder(path)
	r.uploads.CancelChildUploads(path)
	r.deletes.TakeoverDirectory(path)
	return nil
}

// Compile-time assertions for the remove adapters.
var _ mutation.RemoveResolver = vfsRenameResolver{}
var _ mutation.RemoveCleanup = vfsRemoveCleanup{}
var _ mutation.DeleteScheduler = vfsRemoveScheduler{}
