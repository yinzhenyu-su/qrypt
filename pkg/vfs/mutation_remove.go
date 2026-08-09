package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/vfs/mutation"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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

type vfsRemoveScheduler struct{ v *VFS }

func newVFSRemoveScheduler(v *VFS) vfsRemoveScheduler { return vfsRemoveScheduler{v: v} }

func (r vfsRemoveScheduler) ScheduleDelete(path string, entry drive.Entry) {
	r.v.scheduleDelete(path, entry)
}

// vfsRemoveCleanup adapts the pending-upload removal surface to
// mutation.RemoveCleanup. It owns the pending-store/hash cleanup directly
// (the former vfsRemoveRuntime is inlined here).

type vfsRemoveCleanup struct {
	v *VFS
}

func newVFSRemoveCleanup(v *VFS) vfsRemoveCleanup {
	return vfsRemoveCleanup{v: v}
}

func (r vfsRemoveCleanup) RemovePendingFile(path string) (bool, error) {
	// Take the path lock FIRST and re-check inside it: a pending upload
	// that completes between a bare probe and the lock would otherwise be
	// reported as a pending-only remove while the uploaded remote entry is
	// never committed or scheduled for deletion.
	unlock := r.v.lockPath(path)
	defer unlock()
	if _, err := r.v.pendingUpload(path); err != nil {
		return false, nil
	}
	// Durable store commit FIRST; timer/hash/modtime cleanup runs only
	// after it succeeds, so a failed commit never leaves the path half
	// cleaned.
	if err := r.v.uploads.Store().RemoveUpload(path); err != nil {
		return true, err
	}
	r.v.cancelUpload(path)
	r.v.clearLocalModTime(path)
	r.v.hashes.removePath(path)
	return true, nil
}

func (r vfsRemoveCleanup) PrepareDirectory(path string) error {
	// Durable cleanup (pending store + hashes) comes first; timers are
	// cancelled only after it succeeds, so a failed cleanup never leaves
	// the directory visible with its upload timers already cancelled.
	if err := r.v.uploads.Store().RemoveUploadsUnder(path); err != nil {
		return err
	}
	r.v.hashes.removeUnder(path)
	r.v.uploads.CancelChildUploads(path)
	r.v.cancelChildDeletes(path)
	return nil
}

// Compile-time assertions for the remove adapters.
var _ mutation.RemoveResolver = vfsRenameResolver{}
var _ mutation.RemoveCleanup = vfsRemoveCleanup{}
var _ mutation.DeleteScheduler = vfsRemoveScheduler{}
