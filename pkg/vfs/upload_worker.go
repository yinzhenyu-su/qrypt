package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/upload"
)

func (v *VFS) uploadWorker(ctx context.Context) {
	runtime := newVFSUploadWorkerRuntime(v)
	for {
		pending, ok := runtime.Receive(ctx)
		if !ok {
			runtime.StopUploadTimers()
			runtime.StopDeleteTimers()
			return
		}
		_ = v.uploadPending(ctx, pending)
	}
}
func (v *VFS) uploadPending(ctx context.Context, pending PendingUpload) error {
	return upload.PendingWithRuntime(ctx, pending, newVFSUploadWorkerRuntime(v), v.uploads.WorkerCount(), v.uploads.DefaultDelay())
}

// vfsUploadWorkerRuntime adapts VFS internals to upload.WorkerRuntime.
type vfsUploadWorkerRuntime struct {
	v *VFS
}

func newVFSUploadWorkerRuntime(v *VFS) vfsUploadWorkerRuntime {
	return vfsUploadWorkerRuntime{v: v}
}

func (r vfsUploadWorkerRuntime) Receive(ctx context.Context) (PendingUpload, bool) {
	select {
	case <-ctx.Done():
		return PendingUpload{}, false
	case pending := <-r.v.uploads.Queue():
		return pending, true
	}
}

func (r vfsUploadWorkerRuntime) StopUploadTimers() {
	r.v.uploads.Close()
}

func (r vfsUploadWorkerRuntime) StopDeleteTimers() {
	r.v.deletes.Close()
}

func (r vfsUploadWorkerRuntime) SourceUploadSupported() bool {
	return newVFSDriverRuntime(r.v).HasCapability(drive.CapabilitySourceUploader)
}

func (r vfsUploadWorkerRuntime) LatestUpload(path string) (PendingUpload, bool) {
	return r.v.uploads.Store().UploadByPath(path)
}

func (r vfsUploadWorkerRuntime) RemoveStagingIfUnreferenced(localPath string) {
	r.v.uploads.Store().RemoveStagingIfUnreferenced(localPath)
}

func (r vfsUploadWorkerRuntime) Requeue(pending PendingUpload) {
	r.v.enqueue(pending)
}

func (r vfsUploadWorkerRuntime) RequeueAfter(pending PendingUpload, delay time.Duration) {
	r.v.enqueueAfter(pending, delay)
}

func (r vfsUploadWorkerRuntime) QuietDelay(pending PendingUpload) time.Duration {
	d := r.v.uploadQuietDelay(pending)
	return d
}

func (r vfsUploadWorkerRuntime) QuietWindow(pending PendingUpload) time.Duration {
	return r.v.uploadQuietWindow(pending)
}

func (r vfsUploadWorkerRuntime) TryAcquire(pending PendingUpload, workers int) bool {
	return r.v.uploads.TryAcquire(pending, workers)
}

func (r vfsUploadWorkerRuntime) Release(pending PendingUpload) {
	r.v.uploads.Release(pending)
}

func (r vfsUploadWorkerRuntime) ExecuteUpload(ctx context.Context, pending PendingUpload) error {
	return newUploadEngine(r.v).Execute(ctx, pending)
}

func (r vfsUploadWorkerRuntime) SendUpload(pending PendingUpload) {
	r.v.uploads.Enqueue(pending)
}

// enqueueBlocking sends pending to the upload queue, blocking until the
// record is delivered or the VFS shuts down; returns true when delivered.
func (r vfsUploadWorkerRuntime) enqueueBlocking(pending PendingUpload) bool {
	select {
	case r.v.uploads.Queue() <- pending:
		return true
	case <-r.v.done:
		return false
	}
}

// Compile-time check that the adapter satisfies upload.WorkerRuntime.
var _ upload.WorkerRuntime = (*vfsUploadWorkerRuntime)(nil)
