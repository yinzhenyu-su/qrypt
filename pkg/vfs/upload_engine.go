package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"path/filepath"
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
		pending:  newUploadStoreAdapter(v.uploads),
		runtime:  newVFSUploadRuntime(v),
		snapshot: newVFSUploadSnapshotter(v),
		faults:   newVFSUploadFaultController(v),
	}
}

func (e uploadEngine) Execute(ctx context.Context, pending PendingUpload) error {
	observer := e.observer
	pendingStore := e.pending
	runtime := e.runtime
	snapshotter := e.snapshot
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
	observer.State(pending.Path, uploadSnapshotStatePreparing)
	phaseStart := timeutil.Now()
	snapshot, err := snapshotter.SnapshotPending(pending)
	hashNames := uploadSnapshotHashNames(snapshot.Hashes)
	hashSource := "snapshot"
	if snapshot.Incremental {
		hashSource = "incremental"
	}
	snapshotExtra := map[string]any{"hashes": hashNames, "hash_source": hashSource}
	if err != nil {
		snapshotExtra["error"] = err.Error()
	}
	observer.Metadata(pending.Path, "", hashNames)
	observer.Event(pending.Path, "snapshot_hash", phaseStart, pending.Size, snapshotExtra)
	if err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload snapshot failed path=%q local=%q err=%v", pending.Path, pending.LocalPath, err)
		return err
	}
	if latest, ok := pendingStore.UploadByPath(pending.Path); !ok {
		logging.L.DebugfEvery("vfs.skip_upload_removed_after_snapshot", time.Second, "[VFS] skip upload after snapshot; pending removed op_id=%q path=%q", pending.FID, pending.Path)
		pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
		return nil
	} else if !sameUploadRecord(latest, pending) {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_superseded_after_snapshot", time.Second, "[VFS] upload superseded after snapshot op_id=%q path=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.Size, latest.Size)
		pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
		runtime.RequeueIfFrozen(latest)
		return nil
	}
	var uploadName string
	var replaceExisting []drive.Entry
	needsReplace := false
	alreadyReplaced := false
	observer.State(pending.Path, "prepare_remote")
	phaseStart = timeutil.Now()
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
				if drive.IsNonRetryable(err) {
					latest, ok, saveErr := pendingStore.RecordPermanentFailureIfUnchanged(pending, err)
					if saveErr != nil {
						logging.L.Warnf("[VFS] upload failed permanently and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
					} else if ok {
						logging.L.WarnfEvery("vfs.upload_failed_permanent", time.Second, "[VFS] upload failed permanently; not retrying op_id=%q path=%q name=%q size=%d retry=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, err)
					}
					if !ok {
						pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
					}
				} else {
					latest, ok, saveErr := pendingStore.RecordFailureIfUnchanged(pending, err, runtime.RetryDelay(pending.RetryCount+1))
					if saveErr != nil {
						logging.L.Warnf("[VFS] upload failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
					} else if ok {
						logging.L.WarnfEvery("vfs.upload_failed_requeue", time.Second, "[VFS] upload failed; requeue op_id=%q path=%q name=%q size=%d retry=%d next_attempt=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, latest.NextAttemptAt, err)
						runtime.Requeue(latest)
					}
					if !ok {
						pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
					}
				}
			}
			return err
		}
	}
	if err := validateUploadedEntry(entry, uploadName, pending.Size); err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload returned invalid entry op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		if ctx.Err() == nil {
			latest, ok, saveErr := pendingStore.RecordFailureIfUnchanged(pending, err, runtime.RetryDelay(pending.RetryCount+1))
			if saveErr != nil {
				logging.L.Warnf("[VFS] upload validation failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
			} else if ok {
				runtime.Requeue(latest)
			}
			if !ok {
				pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
			}
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
			if e.remote.CanWrite() && ctx.Err() == nil {
				_ = e.remote.Remove(context.WithoutCancel(ctx), entry)
			}
			pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
			if latest, ok := pendingStore.UploadByPath(pending.Path); ok {
				runtime.RequeueIfFrozen(latest)
			}
			return nil
		}
		pending = latest
	}
	if latest, ok := pendingStore.UploadByPath(pending.Path); !ok || !sameUploadRecord(latest, pending) {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed", time.Second, "[VFS] upload committed stale version; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		if e.remote.CanWrite() && ctx.Err() == nil {
			_ = e.remote.Remove(context.WithoutCancel(ctx), entry)
		}
		pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
		if ok {
			runtime.RequeueIfFrozen(latest)
		}
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
				latest, ok, saveErr := pendingStore.RecordFailureIfUnchanged(pending, err, runtime.RetryDelay(pending.RetryCount+1))
				if saveErr != nil {
					logging.L.Warnf("[VFS] upload replace failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
				} else if ok {
					runtime.Requeue(latest)
				}
				if !ok {
					pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
				}
			}
			return err
		}
		entry.Name = pending.Name
		observer.Event(pending.Path, "replace_existing", phaseStart, 0, map[string]any{"uploaded_id": entry.ID, "replaced": len(replaceExisting)})
	}
	entry = runtime.ApplyUploadModTime(pending, entry)
	phaseStart = timeutil.Now()
	runtime.SeedReadCache(entry, snapshot.Path)
	observer.Event(pending.Path, "cache_seed", phaseStart, pending.Size, map[string]any{"entry_id": entry.ID})
	phaseStart = timeutil.Now()
	runtime.CommitUploadedEntry(pending.Path, entry)
	removed, err := pendingStore.RemoveIfUnchanged(pending)
	pendingCleanupExtra := map[string]any{"removed": removed}
	if err != nil {
		pendingCleanupExtra["error"] = err.Error()
	}
	observer.Event(pending.Path, "pending_cleanup", phaseStart, 0, pendingCleanupExtra)
	if err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload committed but pending cleanup failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		return err
	}
	if !removed {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed_after_update", time.Second, "[VFS] upload committed stale version after local update; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		if e.remote.CanWrite() && ctx.Err() == nil {
			_ = e.remote.Remove(context.WithoutCancel(ctx), entry)
		}
		pendingStore.RemoveStagingIfUnreferenced(pending.LocalPath)
		if latest, ok := pendingStore.UploadByPath(pending.Path); ok {
			runtime.RequeueIfFrozen(latest)
		}
		return nil
	}
	phaseStart = timeutil.Now()
	stagingErr := pendingStore.RemoveStaging(pending.LocalPath)
	stagingExtra := map[string]any{}
	if stagingErr != nil {
		stagingExtra["error"] = stagingErr.Error()
	}
	observer.Event(pending.Path, "staging_cleanup", phaseStart, 0, stagingExtra)
	finishState = uploadSnapshotStateCompleted
	logging.L.InfofEvery("vfs.upload_complete", time.Second, "[VFS] upload complete op_id=%q path=%q uploaded_id=%q size=%d dur=%s", pending.FID, pending.Path, entry.ID, entry.Size, time.Since(uploadStart))
	return nil
}

type vfsUploadRuntime struct {
	v *VFS
}

func newVFSUploadRuntime(v *VFS) vfsUploadRuntime {
	return vfsUploadRuntime{v: v}
}

func (r vfsUploadRuntime) ClearUploadHashes(fid string) {
	r.v.uploadHashes.removeFID(fid)
}

func (r vfsUploadRuntime) RetryDelay(retryCount int) time.Duration {
	return uploadRetryDelay(retryCount, r.v.uploadDelay)
}

func (r vfsUploadRuntime) Requeue(pending PendingUpload) {
	r.v.enqueue(pending)
}

func (r vfsUploadRuntime) RequeueIfFrozen(pending PendingUpload) {
	if pending.Frozen {
		r.Requeue(pending)
	}
}

// ModTimeFor returns the authoritative mtime for a pending upload (the
// local mod time set via SetModTime), or zero when the backend should use
// the upload time.
func (r vfsUploadRuntime) ModTimeFor(path string) time.Time {
	return r.v.localModTimeFor(path)
}

func (r vfsUploadRuntime) ApplyUploadModTime(pending PendingUpload, entry drive.Entry) drive.Entry {
	if modTime := r.v.localModTimeFor(pending.Path); !modTime.IsZero() {
		entry.ModTime = modTime
		return entry
	}
	if modTime := uploadModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		r.v.setLocalModTime(pending.Path, modTime)
	}
	return entry
}

func (r vfsUploadRuntime) SeedReadCache(entry drive.Entry, localPath string) {
	r.v.seedReadCacheFromStaging(entry, localPath)
}

func (r vfsUploadRuntime) CommitUploadedEntry(path string, entry drive.Entry) {
	r.v.view.mu.Lock()
	r.v.view.entries[path] = entry
	r.v.unhideCopyChild(filepath.Dir(path), entry.Name)
	r.v.invalidateListLocked(filepath.Dir(path))
	r.v.view.mu.Unlock()
}

type vfsUploadObserver struct {
	v *VFS
}

func newVFSUploadObserver(v *VFS) vfsUploadObserver {
	return vfsUploadObserver{v: v}
}

func (o vfsUploadObserver) Start(pending PendingUpload) {
	o.v.startUploadSnapshot(pending)
}

func (o vfsUploadObserver) State(path, state string) {
	o.v.setUploadSnapshotState(path, state)
}

func (o vfsUploadObserver) Extra(path, key string, value any) {
	o.v.setUploadSnapshotExtra(path, key, value)
}

func (o vfsUploadObserver) Metadata(path, resultRemoteID string, hashes []string) {
	o.v.setUploadSnapshotMetadata(path, resultRemoteID, hashes)
}

func (o vfsUploadObserver) Event(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	o.v.recordUploadEvent(path, phase, start, bytes, extra)
}

func (o vfsUploadObserver) Uploaded(path string, n int) {
	o.v.updateUploadSnapshot(path, n)
}

func (o vfsUploadObserver) HealthResult(op string, err error) {
	o.v.healthTracker.RecordResult(op, err)
}

func (o vfsUploadObserver) Finish(path, state, lastError string) {
	o.v.finishUploadSnapshot(path, state, lastError)
}

type uploadObserverProgress struct {
	observer vfsUploadObserver
	path     string
}

func (p uploadObserverProgress) Phase(phase drive.UploadPhase) {
	if p.path != "" && phase != "" {
		p.observer.State(p.path, string(phase))
	}
}

func (p uploadObserverProgress) Uploaded(n int64) {
	if p.path != "" && n > 0 {
		p.observer.Uploaded(p.path, int(n))
	}
}

type vfsUploadFaultController struct {
	v *VFS
}

func newVFSUploadFaultController(v *VFS) vfsUploadFaultController {
	return vfsUploadFaultController{v: v}
}

func (c vfsUploadFaultController) ApplyCancelFault(ctx context.Context, pending PendingUpload, progress drive.UploadProgress, observer vfsUploadObserver) (context.Context, drive.UploadProgress, func()) {
	fault := c.v.matchUploadCancelFault(pending.Path, pending.FID)
	if fault == nil {
		return ctx, progress, nil
	}
	uploadCtx, uploadCancel := context.WithCancel(ctx)
	cancelProgress := &debugUploadCancelProgress{
		inner:      progress,
		fault:      fault,
		cancel:     uploadCancel,
		cancelPath: pending.Path,
		cancelOpID: pending.FID,
		v:          c.v,
	}
	observer.Extra(pending.Path, "debug_upload_cancel_fault", fault.id)
	cleanup := func() {
		cancelProgress.Close()
		uploadCancel()
	}
	return uploadCtx, cancelProgress, cleanup
}

type vfsUploadScheduler struct {
	v *VFS
}

func newVFSUploadScheduler(v *VFS) vfsUploadScheduler {
	return vfsUploadScheduler{v: v}
}

func (s vfsUploadScheduler) Schedule(pending PendingUpload, delay time.Duration) {
	s.v.uploadSchedule.mu.Lock()
	if timer := s.v.uploadSchedule.timers[pending.Path]; timer != nil {
		timer.Stop()
		logging.L.DebugfEvery("vfs.reschedule_upload", time.Second, "[VFS] reschedule upload op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
	} else {
		logging.L.DebugfEvery("vfs.schedule_upload", time.Second, "[VFS] schedule upload op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
	}
	s.v.uploadSchedule.timers[pending.Path] = time.AfterFunc(delay, func() {
		s.v.uploadSchedule.mu.Lock()
		delete(s.v.uploadSchedule.timers, pending.Path)
		s.v.uploadSchedule.mu.Unlock()
		s.v.sendUpload(pending)
	})
	s.v.uploadSchedule.mu.Unlock()
}

func (s vfsUploadScheduler) Cancel(path string) {
	path = cleanVirtual(path)
	s.v.uploadSchedule.mu.Lock()
	if timer := s.v.uploadSchedule.timers[path]; timer != nil {
		timer.Stop()
		delete(s.v.uploadSchedule.timers, path)
	}
	s.v.uploadSchedule.mu.Unlock()
}

func (s vfsUploadScheduler) CancelChildren(dir string) {
	dir = cleanVirtual(dir)
	s.v.uploadSchedule.mu.Lock()
	for path, timer := range s.v.uploadSchedule.timers {
		if path == dir || isPathUnder(path, dir) {
			timer.Stop()
			delete(s.v.uploadSchedule.timers, path)
		}
	}
	s.v.uploadSchedule.mu.Unlock()
}

func (s vfsUploadScheduler) StopAll() {
	s.v.uploadSchedule.mu.Lock()
	defer s.v.uploadSchedule.mu.Unlock()
	for path, timer := range s.v.uploadSchedule.timers {
		timer.Stop()
		delete(s.v.uploadSchedule.timers, path)
	}
}

func (s vfsUploadScheduler) TimerPaths() map[string]bool {
	paths := map[string]bool{}
	s.v.uploadSchedule.mu.Lock()
	for path := range s.v.uploadSchedule.timers {
		paths[path] = true
	}
	s.v.uploadSchedule.mu.Unlock()
	return paths
}
