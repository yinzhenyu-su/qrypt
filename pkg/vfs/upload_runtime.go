package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
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

func newVFSUploadSnapshotter(v *VFS) vfsUploadSnapshotter {
	return vfsUploadSnapshotter{
		snapshotPending: v.snapshotPending,
	}
}

func (s vfsUploadSnapshotter) SnapshotPending(pending PendingUpload) (upload.Snapshot, error) {
	return s.snapshotPending(pending)
}

func newVFSUploadWriteHashTracker(uploads *upload.Domain, driver drive.Driver) vfsUploadWriteHashTracker {
	return vfsUploadWriteHashTracker{uploads: uploads, driver: driver}
}

func (t vfsUploadWriteHashTracker) Start(pending PendingUpload) {
	t.uploads.StartHashes(pending, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Write(pending PendingUpload, data []byte, off int64) {
	t.uploads.WriteHashes(pending, data, off, requiredUploadSnapshotHashes(t.driver))
}

func (t vfsUploadWriteHashTracker) Dirty(pending PendingUpload) {
	t.uploads.DirtyHashes(pending)
}

func (t vfsUploadWriteHashTracker) RemoveFID(fid string) {
	t.uploads.RemoveHashesByFID(fid)
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
