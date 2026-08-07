package upload

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// rollbackUploadedEntry removes a just-uploaded entry that must not be
// committed because the pending record moved on, drops unreferenced
// staging, and requeues the current pending if it is frozen.
func (e *Engine) rollbackUploadedEntry(ctx context.Context, pending PendingUpload, entry drive.Entry) {
	if e.remote.CanWrite() && ctx.Err() == nil {
		_ = e.remote.Remove(context.WithoutCancel(ctx), entry)
	}
	e.pending.RemoveStagingIfUnreferenced(pending.LocalPath)
	if latest, ok := e.pending.UploadByPath(pending.Path); ok {
		e.runtime.RequeueIfFrozen(latest)
	}
}

// finalizeUpload commits a successfully uploaded entry: seed the read
// cache, commit the entry to the view, and clean up the pending record
// and staging file. When the pending record moved on during the commit
// the uploaded entry is rolled back instead. Returns the finish state
// (one of the SnapshotState* constants), the finish error text, and
// the error to return to the caller.
func (e *Engine) finalizeUpload(ctx context.Context, pending PendingUpload, entry drive.Entry, snapshot Snapshot, uploadStart time.Time) (string, string, error) {
	observer := e.observer
	pendingStore := e.pending
	phaseStart := timeutil.Now()
	e.runtime.SeedReadCache(entry, snapshot.Path)
	observer.Event(pending.Path, "cache_seed", phaseStart, pending.Size, map[string]any{"entry_id": entry.ID})
	phaseStart = timeutil.Now()
	e.runtime.CommitUploadedEntry(pending.Path, entry)
	removed, err := pendingStore.RemoveIfUnchanged(pending)
	pendingCleanupExtra := map[string]any{"removed": removed}
	if err != nil {
		pendingCleanupExtra["error"] = err.Error()
	}
	observer.Event(pending.Path, "pending_cleanup", phaseStart, 0, pendingCleanupExtra)
	if err != nil {
		logging.L.Warnf("[VFS] upload committed but pending cleanup failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		return SnapshotStateFailed, err.Error(), err
	}
	if !removed {
		logging.L.InfofEvery("vfs.upload_stale_committed_after_update", time.Second, "[VFS] upload committed stale version after local update; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		e.rollbackUploadedEntry(ctx, pending, entry)
		return SnapshotStateSuperseded, "", nil
	}
	phaseStart = timeutil.Now()
	stagingErr := pendingStore.RemoveStaging(pending.LocalPath)
	stagingExtra := map[string]any{}
	if stagingErr != nil {
		stagingExtra["error"] = stagingErr.Error()
	}
	observer.Event(pending.Path, "staging_cleanup", phaseStart, 0, stagingExtra)
	logging.L.InfofEvery("vfs.upload_complete", time.Second, "[VFS] upload complete op_id=%q path=%q uploaded_id=%q size=%d dur=%s", pending.FID, pending.Path, entry.ID, entry.Size, time.Since(uploadStart))
	return SnapshotStateCompleted, "", nil
}
