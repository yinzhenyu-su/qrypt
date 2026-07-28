package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"
	"time"
)

const (
	maxUploadRetryDelay       = 15 * time.Minute
	largeUploadQuietThreshold = 16 << 20
	largeUploadQuietDelay     = uploadDebounceDelay
)

func (v *VFS) enqueue(p PendingUpload) {
	if p.PermanentFail {
		logging.L.WarnfEvery("vfs.enqueue_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q size=%d retry=%d last_error=%q", p.FID, p.Path, p.Size, p.RetryCount, p.LastError)
		return
	}
	v.enqueueAfter(p, uploadDelayForRecord(p, v.uploadDelay))
}
func (v *VFS) enqueueAfter(p PendingUpload, delay time.Duration) {
	if delay > 0 {
		v.scheduleUpload(p, delay)
		return
	}
	v.sendUpload(p)
}
func (v *VFS) scheduleUpload(p PendingUpload, delay time.Duration) {
	newVFSUploadScheduler(v).Schedule(p, delay)
}
func (v *VFS) cancelUpload(path string) {
	newVFSUploadScheduler(v).Cancel(path)
}
func (v *VFS) cancelChildUploads(dir string) {
	newVFSUploadScheduler(v).CancelChildren(dir)
}
func (v *VFS) stopUploadTimers() {
	newVFSUploadScheduler(v).StopAll()
}
func (v *VFS) sendUpload(p PendingUpload) {
	newVFSUploadWorkerRuntime(v).SendUpload(p)
}
func uploadDelayForRecord(p PendingUpload, fallback time.Duration) time.Duration {
	if p.NextAttemptAt <= 0 {
		return fallback
	}
	next := time.Unix(0, p.NextAttemptAt)
	if delay := time.Until(next); delay > 0 {
		return delay
	}
	return 0
}
func (v *VFS) uploadQuietDelay(p PendingUpload) time.Duration {
	quietWindow := v.uploadQuietWindow(p)
	if quietWindow <= 0 || p.UpdatedAt <= 0 {
		return 0
	}
	quietFor := time.Since(time.Unix(0, p.UpdatedAt))
	if quietFor >= quietWindow {
		return 0
	}
	return quietWindow - quietFor
}
func (v *VFS) uploadQuietWindow(p PendingUpload) time.Duration {
	quietWindow := v.uploadDelay
	if p.Size >= largeUploadQuietThreshold && quietWindow < largeUploadQuietDelay {
		quietWindow = largeUploadQuietDelay
	}
	return quietWindow
}
func uploadRetryDelay(retryCount int, minimum time.Duration) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	delay := retry.ExponentialBackoff(retryCount - 1)
	if delay < minimum {
		delay = minimum
	}
	if delay > maxUploadRetryDelay {
		delay = maxUploadRetryDelay
	}
	return delay
}
