package upload

import (
	"context"
	"fmt"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
)

// WorkerRuntime is what the worker goroutine needs from the VFS side.
type WorkerRuntime interface {
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
}

// PendingWithRuntime processes one pending upload through its lifecycle:
// driver capability, latest-record check, quiet window, admission, then
// execution via the runtime's ExecuteUpload.
func PendingWithRuntime(ctx context.Context, pending PendingUpload, runtime WorkerRuntime, workers int, fallbackDelay time.Duration) error {
	if !runtime.SourceUploadSupported() {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	latest, ok := runtime.LatestUpload(pending.Path)
	if !ok {
		runtime.RemoveStagingIfUnreferenced(pending.LocalPath)
		return nil
	}
	if !SameUploadRecord(latest, pending) {
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
