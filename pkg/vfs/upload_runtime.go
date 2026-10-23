package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"io"
	"time"
)

// Upload runtime adapters: implementations of the upload-package
// interfaces over VFS internals (observer, fault controller, worker
// runtime, write-store tracker/remote, invalidations, snapshotter).

func (i vfsUploadInvalidations) PublishStalePath(path string) {
	i.invalidations.emit(vfstypes.CleanVirtualPath(path))
}

func newVFSUploadFaultController(faults *faultinject.Registry) vfsUploadFaultController {
	return vfsUploadFaultController{faults: faults}
}

func (c vfsUploadFaultController) ApplyCancelFault(ctx context.Context, pending PendingUpload, progress drive.UploadProgress, observer upload.Observer) (context.Context, drive.UploadProgress, func()) {
	fault, ok := c.faults.Match(time.Now(), pending.Path, pending.FID)
	if !ok {
		return ctx, progress, nil
	}
	uploadCtx, uploadCancel := context.WithCancel(ctx)
	cancelProgress := c.faults.NewProgress(fault, progress, uploadCancel)
	observer.Extra(pending.Path, "debug_upload_cancel_fault", fault.ID)
	cleanup := func() {
		cancelProgress.Close()
		uploadCancel()
	}
	return uploadCtx, cancelProgress, cleanup
}

func newVFSUploadRuntime(v *VFS) vfsUploadRuntime {
	return vfsUploadRuntime{
		uploads:           v.uploads,
		viewRT:            view.NewRuntime(v.view),
		schedulingEnabled: v.uploadSchedulingEnabled,
	}
}

func (r vfsUploadRuntime) ClearUploadHashes(fid string) {
	r.uploads.hashes.RemoveFID(fid)
}

func (r vfsUploadRuntime) RetryDelay(retryCount int) time.Duration {
	return upload.RetryDelay(retryCount, r.uploads.DefaultDelay())
}

func (r vfsUploadRuntime) Requeue(pending PendingUpload) {
	if r.schedulingEnabled() {
		r.uploads.Enqueue(pending)
	}
}

func (r vfsUploadRuntime) RequeueIfFrozen(pending PendingUpload) {
	if pending.Frozen {
		r.Requeue(pending)
	}
}

func (r vfsUploadRuntime) ApplyUploadModTime(pending PendingUpload, entry drive.Entry) drive.Entry {
	if modTime := r.viewRT.LocalModTimeFor(pending.Path); !modTime.IsZero() {
		entry.ModTime = modTime
		return entry
	}
	if modTime := pendingModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		r.viewRT.SetLocalModTime(pending.Path, modTime)
	}
	return entry
}

func newVFSUploadObserver(svc *uploadService, tracker *drive.HealthTracker) vfsUploadObserver {
	return vfsUploadObserver{svc: svc, tracker: tracker}
}

func (o vfsUploadObserver) Start(pending PendingUpload) {
	o.svc.DebugState().Start(pending)
}

func (o vfsUploadObserver) State(path, state string) {
	o.svc.DebugState().SetState(path, state)
}

func (o vfsUploadObserver) Extra(path, key string, value any) {
	o.svc.DebugState().SetExtra(path, key, value)
}

func (o vfsUploadObserver) Metadata(path, resultRemoteID string, hashes []string) {
	o.svc.DebugState().SetMetadata(path, resultRemoteID, hashes)
}

func (o vfsUploadObserver) Event(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	o.svc.DebugState().RecordEvent(path, phase, start, bytes, extra)
}

func (o vfsUploadObserver) Uploaded(path string, n int) {
	o.svc.DebugState().UpdateBytes(path, n)
}

func (o vfsUploadObserver) HealthResult(op string, err error) {
	o.tracker.RecordResult(op, err)
}

func (o vfsUploadObserver) Finish(path, state, lastError string) {
	o.svc.DebugState().Finish(path, state, lastError)
}

func newVFSUploadSnapshotter(v *VFS) vfsUploadSnapshotter {
	return vfsUploadSnapshotter{
		snapshotPending: v.snapshotPending,
	}
}

func (s vfsUploadSnapshotter) SnapshotPending(pending PendingUpload) (upload.Snapshot, error) {
	return s.snapshotPending(pending)
}

func newVFSUploadWriteHashTracker(uploads *uploadDomain, driver drive.Driver) vfsUploadWriteHashTracker {
	return vfsUploadWriteHashTracker{uploads: uploads, driver: driver}
}

func (t vfsUploadWriteHashTracker) Start(pending PendingUpload) {
	t.uploads.hashes.Start(pending, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Write(pending PendingUpload, data []byte, off int64) {
	t.uploads.hashes.Write(pending, data, off, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Dirty(pending PendingUpload) {
	t.uploads.hashes.Dirty(pending)
}

func (t vfsUploadWriteHashTracker) RemoveFID(fid string) {
	t.uploads.hashes.RemoveFID(fid)
}

type uploadWriteRemote interface {
	Parent(ctx context.Context, path string) (drive.Entry, string, error)
	Resolve(ctx context.Context, path string) (drive.Entry, error)
	Read(ctx context.Context, entry drive.Entry) (io.ReadCloser, error)
	InvalidateReadCache(entry drive.Entry)
}

func newVFSUploadWriteRemote(v *VFS) vfsUploadWriteRemote {
	return vfsUploadWriteRemote{resolver: v, driver: v.driver, invalidator: v}
}

func (r vfsUploadWriteRemote) Parent(ctx context.Context, path string) (drive.Entry, string, error) {
	return r.resolver.parent(ctx, path)
}

func (r vfsUploadWriteRemote) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.resolver.resolve(ctx, path)
}

func (r vfsUploadWriteRemote) Read(ctx context.Context, entry drive.Entry) (io.ReadCloser, error) {
	return r.driver.Read(ctx, entry, 0, 0)
}

func (r vfsUploadWriteRemote) InvalidateReadCache(entry drive.Entry) {
	r.invalidator.invalidateReadCache(entry)
}
