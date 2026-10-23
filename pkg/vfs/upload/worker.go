package upload

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// ProcessOptions are the only facts the upload domain cannot own: whether the
// mounted driver accepts source uploads, whether the owning VFS lifecycle is
// running, and the remote execution step.
type ProcessOptions struct {
	SourceUploadSupported bool
	SchedulingEnabled     func() bool
	Execute               func(context.Context, PendingUpload) error
}

// ProcessPending advances one pending generation through latest-record,
// quiet-window and admission checks, then executes it. Queue, store, staging
// cleanup and admission are implementation details owned by Service.
func (s *Service) ProcessPending(ctx context.Context, pending PendingUpload, opts ProcessOptions) error {
	if !opts.SourceUploadSupported {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	if opts.Execute == nil {
		return fmt.Errorf("vfs: upload executor is required")
	}
	latest, ok := s.store.UploadByPath(pending.Path)
	if !ok {
		logging.L.DebugfEvery("vfs.upload_dropped_missing_record", time.Second, "[VFS] drop upload; pending record no longer exists op_id=%q path=%q size=%d", pending.FID, pending.Path, pending.Size)
		s.store.RemoveStagingIfUnreferenced(pending.LocalPath)
		return nil
	}
	if !SameUploadRecord(latest, pending) {
		logging.L.InfofEvery("vfs.upload_superseded", time.Second, "[VFS] upload superseded op_id=%q path=%q old_local=%q new_local=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.LocalPath, latest.LocalPath, pending.Size, latest.Size)
		s.store.RemoveStagingIfUnreferenced(pending.LocalPath)
		if latest.Frozen && schedulingEnabled(opts.SchedulingEnabled) {
			s.Enqueue(latest)
		}
		return nil
	}
	if delay := s.QuietDelay(pending); delay > 0 {
		logging.L.DebugfEvery("vfs.upload_wait_for_quiet", time.Second, "[VFS] upload delayed until writes are quiet op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		if schedulingEnabled(opts.SchedulingEnabled) {
			s.EnqueueAfter(pending, delay)
		}
		return nil
	}
	if !s.TryAcquire(pending, s.workers) {
		delay := s.QuietWindow(pending)
		if delay <= 0 {
			delay = s.delay
		}
		// Content is already quiet. Waiting for admission must not restart
		// a thirty-second (or five-minute) content-stability window.
		if delay <= 0 || delay > 250*time.Millisecond {
			delay = 250 * time.Millisecond
		}
		logging.L.DebugfEvery("vfs.upload_wait_for_admission", time.Second, "[VFS] upload delayed until admission is available op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		if schedulingEnabled(opts.SchedulingEnabled) {
			s.EnqueueAfter(pending, delay)
		}
		return nil
	}
	defer s.Release(pending)
	return opts.Execute(ctx, pending)
}

func schedulingEnabled(enabled func() bool) bool {
	return enabled == nil || enabled()
}

// SameUploadRecord reports whether two pending records describe the same
// upload generation.
func SameUploadRecord(a, b PendingUpload) bool {
	return a.Path == b.Path &&
		a.FID == b.FID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.LocalPath == b.LocalPath &&
		a.Size == b.Size &&
		a.ModTime == b.ModTime &&
		a.UpdatedAt == b.UpdatedAt &&
		a.RetryCount == b.RetryCount &&
		a.LastError == b.LastError &&
		a.PermanentFail == b.PermanentFail &&
		a.LastAttemptAt == b.LastAttemptAt &&
		a.NextAttemptAt == b.NextAttemptAt &&
		SameUploadReplacement(a.ReplaceUpload, b.ReplaceUpload)
}

// SameUploadReplacement compares two replacement descriptors.
func SameUploadReplacement(a, b *UploadReplacement) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.Size == b.Size
}
