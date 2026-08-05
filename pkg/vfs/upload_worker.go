package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"time"
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
	return uploadPendingWithRuntime(ctx, pending, newVFSUploadWorkerRuntime(v), v.uploadWorkers, v.uploadDelay)
}

func uploadPendingWithRuntime(ctx context.Context, pending PendingUpload, runtime uploadWorkerRuntime, workers int, fallbackDelay time.Duration) error {
	if !runtime.SourceUploadSupported() {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	latest, ok := runtime.LatestUpload(pending.Path)
	if !ok {
		logging.L.DebugfEvery("vfs.skip_upload_removed", time.Second, "[VFS] skip upload; pending already removed op_id=%q path=%q local=%q", pending.FID, pending.Path, pending.LocalPath)
		runtime.RemoveStagingIfUnreferenced(pending.LocalPath)
		return nil
	}
	if !sameUploadRecord(latest, pending) {
		logging.L.InfofEvery("vfs.upload_superseded", time.Second, "[VFS] upload superseded op_id=%q path=%q old_local=%q new_local=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.LocalPath, latest.LocalPath, pending.Size, latest.Size)
		runtime.RemoveStagingIfUnreferenced(pending.LocalPath)
		if latest.Frozen {
			runtime.Requeue(latest)
		}
		return nil
	}
	if delay := runtime.QuietDelay(pending); delay > 0 {
		logging.L.DebugfEvery("vfs.upload_wait_for_quiet", time.Second, "[VFS] upload delayed until writes are quiet op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		runtime.RequeueAfter(pending, delay)
		return nil
	}
	if !runtime.TryAcquire(pending, workers) {
		delay := runtime.QuietWindow(pending)
		if delay <= 0 {
			delay = fallbackDelay
		}
		logging.L.DebugfEvery("vfs.upload_wait_for_admission", time.Second, "[VFS] upload delayed until admission is available op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		runtime.RequeueAfter(pending, delay)
		return nil
	}
	defer runtime.Release(pending)
	return runtime.ExecuteUpload(ctx, pending)
}

type uploadWorkerRuntime interface {
	Receive(ctx context.Context) (PendingUpload, bool)
	StopUploadTimers()
	StopDeleteTimers()
	SourceUploadSupported() bool
	LatestUpload(path string) (PendingUpload, bool)
	RemoveStagingIfUnreferenced(localPath string)
	Requeue(pending PendingUpload)
	RequeueAfter(pending PendingUpload, delay time.Duration)
	QuietDelay(pending PendingUpload) time.Duration
	QuietWindow(pending PendingUpload) time.Duration
	TryAcquire(pending PendingUpload, workers int) bool
	Release(pending PendingUpload)
	ExecuteUpload(ctx context.Context, pending PendingUpload) error
	SendUpload(pending PendingUpload)
}

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
	case pending := <-r.v.uploadQueue:
		return pending, true
	}
}

func (r vfsUploadWorkerRuntime) StopUploadTimers() {
	r.v.stopUploadTimers()
}

func (r vfsUploadWorkerRuntime) StopDeleteTimers() {
	r.v.stopDeleteTimers()
}

func (r vfsUploadWorkerRuntime) SourceUploadSupported() bool {
	return newVFSDriverRuntime(r.v).HasCapability(drive.CapabilitySourceUploader)
}

func (r vfsUploadWorkerRuntime) LatestUpload(path string) (PendingUpload, bool) {
	return r.v.uploads.UploadByPath(path)
}

func (r vfsUploadWorkerRuntime) RemoveStagingIfUnreferenced(localPath string) {
	r.v.uploads.removeStagingIfUnreferenced(localPath)
}

func (r vfsUploadWorkerRuntime) Requeue(pending PendingUpload) {
	r.v.enqueue(pending)
}

func (r vfsUploadWorkerRuntime) RequeueAfter(pending PendingUpload, delay time.Duration) {
	r.v.enqueueAfter(pending, delay)
}

func (r vfsUploadWorkerRuntime) QuietDelay(pending PendingUpload) time.Duration {
	return r.v.uploadQuietDelay(pending)
}

func (r vfsUploadWorkerRuntime) QuietWindow(pending PendingUpload) time.Duration {
	return r.v.uploadQuietWindow(pending)
}

func (r vfsUploadWorkerRuntime) TryAcquire(pending PendingUpload, workers int) bool {
	return r.v.uploadAdmit.tryAcquire(pending, workers)
}

func (r vfsUploadWorkerRuntime) Release(pending PendingUpload) {
	r.v.uploadAdmit.release(pending)
}

func (r vfsUploadWorkerRuntime) ExecuteUpload(ctx context.Context, pending PendingUpload) error {
	return newUploadEngine(r.v).Execute(ctx, pending)
}

func (r vfsUploadWorkerRuntime) SendUpload(pending PendingUpload) {
	select {
	case r.v.uploadQueue <- pending:
		logging.L.DebugfEvery("vfs.upload_enqueued", time.Second, "[VFS] upload enqueued op_id=%q path=%q size=%d retry=%d", pending.FID, pending.Path, pending.Size, pending.RetryCount)
	default:
		logging.L.Warnf("[VFS] upload queue full; blocking enqueue in background op_id=%q path=%q size=%d", pending.FID, pending.Path, pending.Size)
		// Blocking enqueue must also exit on shutdown: the upload workers
		// return on ctx.Done, and without this select the goroutine would
		// block on a full queue forever.
		go func() {
			select {
			case r.v.uploadQueue <- pending:
			case <-r.v.done:
			}
		}()
	}
}
