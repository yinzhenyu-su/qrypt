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

func (i vfsUploadInvalidations) InvalidatePath(path string) {
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
		hashes:            v.hashes,
		uploads:           v.uploads,
		viewRT:            view.NewRuntime(v.view),
		schedulingEnabled: v.uploadSchedulingEnabled,
	}
}

func (r vfsUploadRuntime) ClearUploadHashes(fid string) {
	r.hashes.RemoveFID(fid)
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
	if modTime := uploadModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		r.viewRT.SetLocalModTime(pending.Path, modTime)
	}
	return entry
}

func newVFSUploadObserver(svc *uploadService, tracker *drive.HealthTracker) vfsUploadObserver {
	return vfsUploadObserver{svc: svc, tracker: tracker}
}

func (o vfsUploadObserver) Start(pending PendingUpload) {
	newVFSDebugUploadRuntime(o.svc).StartSnapshot(pending)
}

func (o vfsUploadObserver) State(path, state string) {
	newVFSDebugUploadRuntime(o.svc).SetSnapshotState(path, state)
}

func (o vfsUploadObserver) Extra(path, key string, value any) {
	newVFSDebugUploadRuntime(o.svc).SetSnapshotExtra(path, key, value)
}

func (o vfsUploadObserver) Metadata(path, resultRemoteID string, hashes []string) {
	newVFSDebugUploadRuntime(o.svc).SetSnapshotMetadata(path, resultRemoteID, hashes)
}

func (o vfsUploadObserver) Event(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	newVFSDebugUploadRuntime(o.svc).RecordEvent(path, phase, start, bytes, extra)
}

func (o vfsUploadObserver) Uploaded(path string, n int) {
	newVFSDebugUploadRuntime(o.svc).UpdateSnapshot(path, n)
}

func (o vfsUploadObserver) HealthResult(op string, err error) {
	o.tracker.RecordResult(op, err)
}

func (o vfsUploadObserver) Finish(path, state, lastError string) {
	newVFSDebugUploadRuntime(o.svc).FinishSnapshot(path, state, lastError)
}

func newVFSUploadSnapshotter(v *VFS) vfsUploadSnapshotter {
	return vfsUploadSnapshotter{
		locks:           v.pathLocks,
		store:           v.uploads.Store(),
		hashes:          v.hashes,
		driver:          v.driver,
		snapshotPending: v.snapshotPending,
	}
}

func (s vfsUploadSnapshotter) SnapshotPending(pending PendingUpload) (upload.Snapshot, error) {
	return s.snapshotPending(pending)
}

func newVFSUploadWorkerRuntime(v *VFS) vfsUploadWorkerRuntime {
	return vfsUploadWorkerRuntime{
		uploads:           v.uploads,
		deletes:           v.deletes,
		driver:            v.driver,
		done:              v.done,
		schedulingEnabled: v.uploadSchedulingEnabled,
		engine:            v.uploadEngine,
	}
}

func (r vfsUploadWorkerRuntime) Receive(ctx context.Context) (PendingUpload, bool) {
	select {
	case <-ctx.Done():
		return PendingUpload{}, false
	case pending := <-r.uploads.Queue():
		return pending, true
	}
}

func (r vfsUploadWorkerRuntime) StopUploadTimers() {
	r.uploads.Close()
}

func (r vfsUploadWorkerRuntime) StopDeleteTimers() {
	r.deletes.Close()
}

func (r vfsUploadWorkerRuntime) SourceUploadSupported() bool {
	return drive.HasCapability(r.driver, drive.CapabilitySourceUploader)
}

func (r vfsUploadWorkerRuntime) LatestUpload(path string) (PendingUpload, bool) {
	return r.uploads.Store().UploadByPath(path)
}

func (r vfsUploadWorkerRuntime) RemoveStagingIfUnreferenced(localPath string) {
	r.uploads.Store().RemoveStagingIfUnreferenced(localPath)
}

func (r vfsUploadWorkerRuntime) Requeue(pending PendingUpload) {
	if r.schedulingEnabled() {
		r.uploads.Enqueue(pending)
	}
}

func (r vfsUploadWorkerRuntime) RequeueAfter(pending PendingUpload, delay time.Duration) {
	if r.schedulingEnabled() {
		r.uploads.EnqueueAfter(pending, delay)
	}
}

func (r vfsUploadWorkerRuntime) QuietDelay(pending PendingUpload) time.Duration {
	return r.uploads.QuietDelay(pending)
}

func (r vfsUploadWorkerRuntime) QuietWindow(pending PendingUpload) time.Duration {
	return r.uploads.QuietWindow(pending)
}

func (r vfsUploadWorkerRuntime) TryAcquire(pending PendingUpload, workers int) bool {
	return r.uploads.TryAcquire(pending, workers)
}

func (r vfsUploadWorkerRuntime) Release(pending PendingUpload) {
	r.uploads.Release(pending)
}

func (r vfsUploadWorkerRuntime) ExecuteUpload(ctx context.Context, pending PendingUpload) error {
	return r.engine.Execute(ctx, pending)
}

func (r vfsUploadWorkerRuntime) SendUpload(pending PendingUpload) {
	r.uploads.Enqueue(pending)
}

func newVFSUploadWriteHashTracker(hashes *upload.HashTracker, driver drive.Driver) vfsUploadWriteHashTracker {
	return vfsUploadWriteHashTracker{hashes: hashes, driver: driver}
}

func (t vfsUploadWriteHashTracker) Start(pending PendingUpload) {
	t.hashes.Start(pending, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Write(pending PendingUpload, data []byte, off int64) {
	t.hashes.Write(pending, data, off, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Dirty(pending PendingUpload) {
	t.hashes.Dirty(pending)
}

func (t vfsUploadWriteHashTracker) RemoveFID(fid string) {
	t.hashes.RemoveFID(fid)
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
