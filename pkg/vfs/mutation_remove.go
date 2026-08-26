package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/vfs/pathlock"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
)

func (v *VFS) removeWithRuntime(ctx context.Context, path string, committer viewCommitter) (err error) {
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
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
	defer func() { v.recordHealthResult(drive.HealthOpDelete, err) }()
	if err := newVFSDriverRuntime(v).RequireCapability(drive.CapabilityWriter, "remove"); err != nil {
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
	hashes  *upload.HashTracker
	deletes vfsDeleteScheduler
}

func newVFSRemoveCleanup(v *VFS) vfsRemoveCleanup {
	return vfsRemoveCleanup{
		locks:   v.pathLocks,
		store:   v.uploads.Store(),
		uploads: v.uploads,
		viewRT:  view.NewRuntime(v.view),
		hashes:  v.hashes,
		deletes: newVFSDeleteScheduler(v),
	}
}

func (r vfsRemoveCleanup) RemovePendingFile(path string) (bool, error) {
	// Take the path lock FIRST and re-check inside it: a pending upload
	// that completes between a bare probe and the lock would otherwise be
	// reported as a pending-only remove while the uploaded remote entry is
	// never committed or scheduled for deletion.
	unlock := r.locks.Lock(vfstypes.CleanVirtualPath(path))
	defer unlock()
	if _, ok := r.store.UploadByPath(vfstypes.CleanVirtualPath(path)); !ok {
		return false, nil
	}
	// Durable store commit FIRST; timer/hash/modtime cleanup runs only
	// after it succeeds, so a failed commit never leaves the path half
	// cleaned.
	if err := r.store.RemoveUpload(path); err != nil {
		return true, err
	}
	r.uploads.CancelUpload(path)
	r.viewRT.ClearLocalModTime(path)
	r.hashes.RemovePath(path)
	return true, nil
}

func (r vfsRemoveCleanup) PrepareDirectory(path string) error {
	// Durable cleanup (pending store + hashes) comes first; timers are
	// cancelled only after it succeeds, so a failed cleanup never leaves
	// the directory visible with its upload timers already cancelled.
	if err := r.store.RemoveUploadsUnder(path); err != nil {
		return err
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
