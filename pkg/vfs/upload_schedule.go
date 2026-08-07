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

// --- UploadService schedule methods ---

func (s *UploadService) Enqueue(p PendingUpload) {
	if p.PermanentFail {
		logging.L.WarnfEvery("vfs.enqueue_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q size=%d retry=%d last_error=%q", p.FID, p.Path, p.Size, p.RetryCount, p.LastError)
		return
	}
	s.EnqueueAfter(p, uploadDelayForRecord(p, s.delay))
}

func (s *UploadService) EnqueueAfter(p PendingUpload, delay time.Duration) {
	if delay > 0 {
		s.scheduleUpload(p, delay)
		return
	}
	s.sendUpload(p)
}

func (s *UploadService) CancelUpload(path string) {
	path = cleanVirtual(path)
	s.schedule.mu.Lock()
	if timer := s.schedule.timers[path]; timer != nil {
		timer.Stop()
		delete(s.schedule.timers, path)
	}
	s.schedule.mu.Unlock()
}

func (s *UploadService) CancelChildUploads(dir string) {
	dir = cleanVirtual(dir)
	s.schedule.mu.Lock()
	for path, timer := range s.schedule.timers {
		if path == dir || isPathUnder(path, dir) {
			timer.Stop()
			delete(s.schedule.timers, path)
		}
	}
	s.schedule.mu.Unlock()
}

func (s *UploadService) QuietDelay(p PendingUpload) time.Duration {
	qw := s.quietWindow(p)
	if qw <= 0 || p.UpdatedAt <= 0 {
		return 0
	}
	qf := time.Since(time.Unix(0, p.UpdatedAt))
	if qf >= qw {
		return 0
	}
	return qw - qf
}

func (s *UploadService) quietWindow(p PendingUpload) time.Duration {
	qw := s.delay
	if p.Size >= largeUploadQuietThreshold && qw < largeUploadQuietDelay {
		qw = largeUploadQuietDelay
	}
	return qw
}

func (s *UploadService) scheduleUpload(p PendingUpload, delay time.Duration) {
	s.schedule.mu.Lock()
	if timer := s.schedule.timers[p.Path]; timer != nil {
		timer.Stop()
		logging.L.DebugfEvery("vfs.reschedule_upload", time.Second, "[VFS] reschedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	} else {
		logging.L.DebugfEvery("vfs.schedule_upload", time.Second, "[VFS] schedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	}
	s.schedule.timers[p.Path] = time.AfterFunc(delay, func() {
		s.schedule.mu.Lock()
		delete(s.schedule.timers, p.Path)
		s.schedule.mu.Unlock()
		s.sendUpload(p)
	})
	s.schedule.mu.Unlock()
}

func (s *UploadService) sendUpload(p PendingUpload) {
	select {
	case s.queue <- p:
		logging.L.DebugfEvery("vfs.upload_enqueued", time.Second, "[VFS] upload enqueued op_id=%q path=%q size=%d retry=%d", p.FID, p.Path, p.Size, p.RetryCount)
	default:
		logging.L.Warnf("[VFS] upload queue full; blocking enqueue in background op_id=%q path=%q size=%d", p.FID, p.Path, p.Size)
		done := s.done
		if done == nil {
			return // no lifecycle channel wired; drop to avoid goroutine leak
		}
		go func() {
			select {
			case s.queue <- p:
			case <-done:
			}
		}()
	}
}

// --- VFS wrapper methods (keep for backward compat) ---

func (v *VFS) enqueue(p PendingUpload)                         { v.uploads.Enqueue(p) }
func (v *VFS) enqueueAfter(p PendingUpload, d time.Duration)   { v.uploads.EnqueueAfter(p, d) }
func (v *VFS) cancelUpload(path string)                        { v.uploads.CancelUpload(path) }
func (v *VFS) uploadQuietDelay(p PendingUpload) time.Duration  { return v.uploads.QuietDelay(p) }
func (v *VFS) uploadQuietWindow(p PendingUpload) time.Duration { return v.uploads.quietWindow(p) }

// --- schedule state helpers ---

func (s *uploadScheduleState) timerPaths() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := map[string]bool{}
	for p := range s.timers {
		paths[p] = true
	}
	return paths
}

// TimerPaths returns a snapshot of paths with active schedule timers.
func (svc *UploadService) TimerPaths() map[string]bool {
	return svc.schedule.timerPaths()
}

func (s *uploadScheduleState) stop() {
	s.stopAll()
}

// --- package-level helpers ---

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
