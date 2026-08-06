package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"time"
)

type uploadEngine struct {
	remote   remoteMutationBackend
	observer vfsUploadObserver
	pending  uploadStoreAdapter
	runtime  vfsUploadRuntime
	snapshot vfsUploadSnapshotter
	faults   vfsUploadFaultController
}

func newUploadEngine(v *VFS) uploadEngine {
	return uploadEngine{
		remote:   newVFSDriverRuntime(v).RemoteMutationBackend(),
		observer: newVFSUploadObserver(v),
		pending:  newUploadStoreAdapter(v.upload.store),
		runtime:  newVFSUploadRuntime(v),
		snapshot: newVFSUploadSnapshotter(v),
		faults:   newVFSUploadFaultController(v),
	}
}

func (e uploadEngine) Execute(ctx context.Context, pending PendingUpload) error {
	observer := e.observer
	pendingStore := e.pending
	runtime := e.runtime
	faults := e.faults
	uploadStart := timeutil.Now()
	logging.L.InfofEvery("vfs.upload_start", time.Second, "[VFS] upload start op_id=%q path=%q parent=%q name=%q size=%d local=%q retry=%d", pending.FID, pending.Path, pending.ParentID, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount)
	observer.Start(pending)
	uploadFID := pending.FID
	if pending.UpdatedAt > 0 {
		queuedAt := time.Unix(0, pending.UpdatedAt)
		if uploadStart.After(queuedAt) {
			observer.Event(pending.Path, "queue_wait", queuedAt, 0, nil)
		}
	}
	observer.Extra(pending.Path, "local_path", pending.LocalPath)
	observer.Extra(pending.Path, "parent_id", pending.ParentID)
	finishState := uploadSnapshotStateFailed
	finishErr := ""
	defer func() {
		if finishState == uploadSnapshotStateCompleted || finishState == uploadSnapshotStateSuperseded {
			runtime.ClearUploadHashes(uploadFID)
		}
		observer.Finish(pending.Path, finishState, finishErr)
	}()

	// Freeze the local file into an upload snapshot and confirm the pending
	// record is still current before touching the remote.
	observer.State(pending.Path, uploadSnapshotStatePreparing)
	snapshot, ok, err := e.freezeSnapshot(pending)
	if err != nil {
		finishErr = err.Error()
		return err
	}
	if !ok {
		return nil
	}
	var uploadName string
	var replaceExisting []drive.Entry
	needsReplace := false
	alreadyReplaced := false
	observer.State(pending.Path, "prepare_remote")
	phaseStart := timeutil.Now()
	replaceUpload := pending.ReplaceUpload
	if target, err := prepareUploadTarget(ctx, e.remote, pending.ParentID, pending.Name, pending.FID, uploadReplacementID(replaceUpload)); err != nil {
		observer.Event(pending.Path, "prepare_remote", phaseStart, 0, map[string]any{"error": err.Error()})
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload remote preparation failed path=%q parent=%q name=%q err=%v", pending.Path, pending.ParentID, pending.Name, err)
		return err
	} else {
		uploadName = target.UploadName
		replaceExisting = target.ReplaceExisting
		alreadyReplaced = target.AlreadyReplaced
		needsReplace = !alreadyReplaced && (replaceUpload != nil || len(replaceExisting) > 0)
		observer.Event(pending.Path, "prepare_remote", phaseStart, 0, map[string]any{"upload_name": uploadName, "replace_existing": len(replaceExisting), "replace_resume": replaceUpload != nil, "already_replaced": target.AlreadyReplaced})
	}
	var entry drive.Entry
	if replaceUpload != nil {
		entry = uploadReplacementEntry(*replaceUpload)
		if alreadyReplaced {
			entry.Name = pending.Name
		}
		uploadName = entry.Name
		observer.Metadata(pending.Path, entry.ID, nil)
	} else {
		observer.State(pending.Path, uploadSnapshotStateUploading)
	}
	source := drive.NewLocalReadOnlyFileSourceWithHashes(snapshot.Path, pending.Size, snapshot.Hashes)
	progress := uploadObserverProgress{observer: observer, path: pending.Path}
	uploadCtx, uploadProgress, cleanupFault := faults.ApplyCancelFault(ctx, pending, progress, observer)
	if cleanupFault != nil {
		defer cleanupFault()
	}
	if replaceUpload == nil {
		phaseStart = timeutil.Now()
		var err error
		entry, err = e.remote.PutSource(uploadCtx, drive.UploadRequest{
			ParentID: pending.ParentID,
			Name:     uploadName,
			Source:   source,
			Progress: uploadProgress,
			ModTime:  e.runtime.ModTimeFor(pending.Path),
		})
		observer.Metadata(pending.Path, entry.ID, nil)
		traceExtra := map[string]any{"entry_id": entry.ID}
		if err != nil {
			traceExtra["error"] = err.Error()
		}
		observer.Event(pending.Path, "driver_put_source", phaseStart, pending.Size, traceExtra)
		observer.HealthResult(drive.HealthOpUpload, err)
		if err != nil {
			finishErr = err.Error()
			if ctx.Err() == nil {
				e.recordFailure(pending, err)
			}
			return err
		}
	}
	if err := validateUploadedEntry(entry, uploadName, pending.Size); err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload returned invalid entry op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		if ctx.Err() == nil {
			e.recordFailure(pending, err)
		}
		return err
	}
	if len(replaceExisting) > 0 && pending.ReplaceUpload == nil {
		latest, ok, saveErr := pendingStore.RecordReplacementIfUnchanged(pending, uploadReplacement(entry))
		if saveErr != nil {
			finishErr = saveErr.Error()
			logging.L.Warnf("[VFS] upload replace state save failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, saveErr)
			return saveErr
		}
		if !ok {
			finishState = uploadSnapshotStateSuperseded
			e.rollbackUploadedEntry(ctx, pending, entry)
			return nil
		}
		pending = latest
	}
	if latest, ok := pendingStore.UploadByPath(pending.Path); !ok || !sameUploadRecord(latest, pending) {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed", time.Second, "[VFS] upload committed stale version; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		e.rollbackUploadedEntry(ctx, pending, entry)
		return nil
	}
	if needsReplace {
		observer.State(pending.Path, "replacing_existing")
		phaseStart = timeutil.Now()
		if err := replaceUploadedFile(ctx, e.remote, entry, replaceExisting, pending.Name); err != nil {
			observer.Event(pending.Path, "replace_existing", phaseStart, 0, map[string]any{"error": err.Error(), "uploaded_id": entry.ID})
			finishErr = err.Error()
			logging.L.Warnf("[VFS] upload replace existing failed op_id=%q path=%q uploaded_id=%q name=%q err=%v", pending.FID, pending.Path, entry.ID, pending.Name, err)
			if ctx.Err() == nil {
				e.recordFailure(pending, err)
			}
			return err
		}
		entry.Name = pending.Name
		observer.Event(pending.Path, "replace_existing", phaseStart, 0, map[string]any{"uploaded_id": entry.ID, "replaced": len(replaceExisting)})
	}
	finishState, finishErr, err = e.finalizeUpload(ctx, pending, entry, snapshot, uploadStart)
	return err
}

// recordFailure persists a failed upload attempt on the pending record.
// Permanent failures are recorded without requeueing; retryable failures
// are requeued with the driver retry delay. When the pending record moved
// on the staging file is dropped. Returns false when the failure could
// not be recorded (save error or record moved on).
func (e uploadEngine) recordFailure(pending PendingUpload, err error) bool {
	if drive.IsNonRetryable(err) {
		_, ok, saveErr := e.pending.RecordPermanentFailureIfUnchanged(pending, err)
		if saveErr != nil {
			logging.L.Warnf("[VFS] upload failed permanently and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
			return false
		}
		if ok {
			logging.L.WarnfEvery("vfs.upload_failed_permanent", time.Second, "[VFS] upload failed permanently; not retrying op_id=%q path=%q name=%q size=%d retry=%d err=%v", pending.FID, pending.Path, pending.Name, pending.Size, pending.RetryCount, err)
		} else {
			e.pending.RemoveStagingIfUnreferenced(pending.LocalPath)
		}
		return ok
	}
	latest, ok, saveErr := e.pending.RecordFailureIfUnchanged(pending, err, e.runtime.RetryDelay(pending.RetryCount+1))
	if saveErr != nil {
		logging.L.Warnf("[VFS] upload failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
		return false
	}
	if ok {
		logging.L.WarnfEvery("vfs.upload_failed_requeue", time.Second, "[VFS] upload failed; requeue op_id=%q path=%q name=%q size=%d retry=%d next_attempt=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, latest.NextAttemptAt, err)
		e.runtime.Requeue(latest)
	} else {
		e.pending.RemoveStagingIfUnreferenced(pending.LocalPath)
	}
	return ok
}
