package vfs

import (
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"
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
	v.enqueueAfter(p, uploadDelayForRecord(p, v.upload.delay))
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
	quietWindow := v.upload.delay
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

type vfsUploadScheduler struct {
	v *VFS
}

func newVFSUploadScheduler(v *VFS) vfsUploadScheduler {
	return vfsUploadScheduler{v: v}
}

func (s vfsUploadScheduler) Schedule(pending PendingUpload, delay time.Duration) {
	s.v.upload.schedule.mu.Lock()
	if timer := s.v.upload.schedule.timers[pending.Path]; timer != nil {
		timer.Stop()
		logging.L.DebugfEvery("vfs.reschedule_upload", time.Second, "[VFS] reschedule upload op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
	} else {
		logging.L.DebugfEvery("vfs.schedule_upload", time.Second, "[VFS] schedule upload op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
	}
	s.v.upload.schedule.timers[pending.Path] = time.AfterFunc(delay, func() {
		s.v.upload.schedule.mu.Lock()
		delete(s.v.upload.schedule.timers, pending.Path)
		s.v.upload.schedule.mu.Unlock()
		s.v.sendUpload(pending)
	})
	s.v.upload.schedule.mu.Unlock()
}

func (s vfsUploadScheduler) Cancel(path string) {
	path = cleanVirtual(path)
	s.v.upload.schedule.mu.Lock()
	if timer := s.v.upload.schedule.timers[path]; timer != nil {
		timer.Stop()
		delete(s.v.upload.schedule.timers, path)
	}
	s.v.upload.schedule.mu.Unlock()
}

func (s vfsUploadScheduler) CancelChildren(dir string) {
	dir = cleanVirtual(dir)
	s.v.upload.schedule.mu.Lock()
	for path, timer := range s.v.upload.schedule.timers {
		if path == dir || isPathUnder(path, dir) {
			timer.Stop()
			delete(s.v.upload.schedule.timers, path)
		}
	}
	s.v.upload.schedule.mu.Unlock()
}

func (s vfsUploadScheduler) StopAll() {
	s.v.upload.schedule.mu.Lock()
	defer s.v.upload.schedule.mu.Unlock()
	for path, timer := range s.v.upload.schedule.timers {
		timer.Stop()
		delete(s.v.upload.schedule.timers, path)
	}
}

func (s vfsUploadScheduler) TimerPaths() map[string]bool {
	paths := map[string]bool{}
	s.v.upload.schedule.mu.Lock()
	for path := range s.v.upload.schedule.timers {
		paths[path] = true
	}
	s.v.upload.schedule.mu.Unlock()
	return paths
}
