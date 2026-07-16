package vfs

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	maxUploadRetryDelay       = 15 * time.Minute
	largeUploadQuietThreshold = 16 << 20
	largeUploadQuietDelay     = uploadDebounceDelay
)

func (v *VFS) Pending() []PendingFile {
	return v.cache.Pending()
}

func (v *VFS) uploadWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			v.stopUploadTimers()
			v.stopDeleteTimers()
			return
		case pending := <-v.queue:
			_ = v.uploadPending(ctx, pending)
		}
	}
}

func (v *VFS) uploadPending(ctx context.Context, pending PendingFile) error {
	if !drive.HasCapability(v.driver, drive.CapabilitySourceUploader) {
		return fmt.Errorf("vfs: driver does not support upload")
	}
	latest, ok := v.cache.PendingByPath(pending.Path)
	if !ok {
		logging.L.DebugfEvery("vfs.skip_upload_removed", time.Second, "[VFS] skip upload; pending already removed op_id=%q path=%q local=%q", pending.FID, pending.Path, pending.LocalPath)
		return nil
	}
	if !samePendingFile(latest, pending) {
		logging.L.InfofEvery("vfs.upload_superseded", time.Second, "[VFS] upload superseded op_id=%q path=%q old_local=%q new_local=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.LocalPath, latest.LocalPath, pending.Size, latest.Size)
		v.enqueue(latest)
		return nil
	}
	if delay := v.pendingQuietDelay(pending); delay > 0 {
		logging.L.DebugfEvery("vfs.upload_wait_for_quiet", time.Second, "[VFS] upload delayed until writes are quiet op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		v.enqueueAfter(pending, delay)
		return nil
	}
	if !v.uploadAdmit.tryAcquire(pending, v.uploadWorkers) {
		delay := v.pendingQuietWindow(pending)
		if delay <= 0 {
			delay = v.uploadDelay
		}
		logging.L.DebugfEvery("vfs.upload_wait_for_admission", time.Second, "[VFS] upload delayed until admission is available op_id=%q path=%q size=%d delay=%s", pending.FID, pending.Path, pending.Size, delay)
		v.enqueueAfter(pending, delay)
		return nil
	}
	defer v.uploadAdmit.release(pending)
	uploadStart := timeutil.Now()
	logging.L.InfofEvery("vfs.upload_start", time.Second, "[VFS] upload start op_id=%q path=%q parent=%q name=%q size=%d local=%q retry=%d", pending.FID, pending.Path, pending.ParentID, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount)
	v.startUploadSnapshot(pending)
	if pending.UpdatedAt > 0 {
		queuedAt := time.Unix(0, pending.UpdatedAt)
		if uploadStart.After(queuedAt) {
			v.recordUploadEvent(pending.Path, "queue_wait", queuedAt, 0, nil)
		}
	}
	v.setUploadSnapshotExtra(pending.Path, "local_path", pending.LocalPath)
	v.setUploadSnapshotExtra(pending.Path, "parent_id", pending.ParentID)
	finishState := uploadSnapshotStateFailed
	finishErr := ""
	defer func() { v.finishUploadSnapshot(pending.Path, finishState, finishErr) }()
	v.setUploadSnapshotState(pending.Path, uploadSnapshotStatePreparing)
	phaseStart := timeutil.Now()
	snapshot, err := v.snapshotPending(pending)
	hashNames := []string{string(drive.HashMD5), string(drive.HashSHA1), string(drive.HashSHA256)}
	snapshotExtra := map[string]any{"hashes": hashNames}
	if err != nil {
		snapshotExtra["error"] = err.Error()
	}
	v.setUploadSnapshotMetadata(pending.Path, "", hashNames)
	v.recordUploadEvent(pending.Path, "snapshot_hash", phaseStart, pending.Size, snapshotExtra)
	if err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload snapshot failed path=%q local=%q err=%v", pending.Path, pending.LocalPath, err)
		return err
	}
	defer os.Remove(snapshot.Path)
	if latest, ok := v.cache.PendingByPath(pending.Path); !ok {
		logging.L.DebugfEvery("vfs.skip_upload_removed_after_snapshot", time.Second, "[VFS] skip upload after snapshot; pending removed op_id=%q path=%q", pending.FID, pending.Path)
		return nil
	} else if !samePendingFile(latest, pending) {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_superseded_after_snapshot", time.Second, "[VFS] upload superseded after snapshot op_id=%q path=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.Size, latest.Size)
		v.enqueue(latest)
		return nil
	}
	uploadName := pending.Name
	var replaceExisting []drive.Entry
	needsReplace := false
	alreadyReplaced := false
	v.setUploadSnapshotState(pending.Path, "prepare_remote")
	phaseStart = timeutil.Now()
	replaceUpload := pending.ReplaceUpload
	if target, err := v.prepareUploadTarget(ctx, pending.ParentID, pending.Name, pending.FID, pendingReplaceUploadID(replaceUpload)); err != nil {
		v.recordUploadEvent(pending.Path, "prepare_remote", phaseStart, 0, map[string]any{"error": err.Error()})
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload remote preparation failed path=%q parent=%q name=%q err=%v", pending.Path, pending.ParentID, pending.Name, err)
		return err
	} else {
		uploadName = target.UploadName
		replaceExisting = target.ReplaceExisting
		alreadyReplaced = target.AlreadyReplaced
		needsReplace = !alreadyReplaced && (replaceUpload != nil || len(replaceExisting) > 0)
		v.recordUploadEvent(pending.Path, "prepare_remote", phaseStart, 0, map[string]any{"upload_name": uploadName, "replace_existing": len(replaceExisting), "replace_resume": replaceUpload != nil, "already_replaced": target.AlreadyReplaced})
	}
	var entry drive.Entry
	if replaceUpload != nil {
		entry = pendingReplaceUploadEntry(*replaceUpload)
		if alreadyReplaced {
			entry.Name = pending.Name
		}
		uploadName = entry.Name
		v.setUploadSnapshotMetadata(pending.Path, entry.ID, nil)
	} else {
		v.setUploadSnapshotState(pending.Path, uploadSnapshotStateUploading)
	}
	source := drive.NewLocalReadOnlyFileSourceWithHashes(snapshot.Path, pending.Size, snapshot.Hashes)
	progress := debugUploadProgress{
		v:    v,
		path: pending.Path,
		update: func(n int64) {
			v.updateUploadSnapshot(pending.Path, int(n))
		},
	}
	if replaceUpload == nil {
		phaseStart = timeutil.Now()
		var err error
		entry, err = v.driver.PutSource(ctx, drive.UploadRequest{
			ParentID: pending.ParentID,
			Name:     uploadName,
			Source:   source,
			Progress: progress,
		})
		v.setUploadSnapshotMetadata(pending.Path, entry.ID, nil)
		traceExtra := map[string]any{"entry_id": entry.ID}
		if err != nil {
			traceExtra["error"] = err.Error()
		}
		v.recordUploadEvent(pending.Path, "driver_put_source", phaseStart, pending.Size, traceExtra)
		v.healthTracker.RecordResult(drive.HealthOpUpload, err)
		if err != nil {
			finishErr = err.Error()
			if ctx.Err() == nil {
				if drive.IsNonRetryable(err) {
					if latest, ok, saveErr := v.cache.RecordPendingPermanentFailure(pending.Path, err); saveErr != nil {
						logging.L.Warnf("[VFS] upload failed permanently and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
					} else if ok {
						logging.L.WarnfEvery("vfs.upload_failed_permanent", time.Second, "[VFS] upload failed permanently; not retrying op_id=%q path=%q name=%q size=%d retry=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, err)
					}
				} else if latest, ok, saveErr := v.cache.RecordPendingFailure(pending.Path, err, uploadRetryDelay(pending.RetryCount+1, v.uploadDelay)); saveErr != nil {
					logging.L.Warnf("[VFS] upload failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
				} else if ok {
					logging.L.WarnfEvery("vfs.upload_failed_requeue", time.Second, "[VFS] upload failed; requeue op_id=%q path=%q name=%q size=%d retry=%d next_attempt=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, latest.NextAttemptAt, err)
					v.enqueue(latest)
				}
			}
			return err
		}
	}
	if err := validateUploadedEntry(entry, uploadName, pending.Size); err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload returned invalid entry op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		if ctx.Err() == nil {
			if latest, ok, saveErr := v.cache.RecordPendingFailure(pending.Path, err, uploadRetryDelay(pending.RetryCount+1, v.uploadDelay)); saveErr != nil {
				logging.L.Warnf("[VFS] upload validation failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
			} else if ok {
				v.enqueue(latest)
			}
		}
		return err
	}
	if len(replaceExisting) > 0 && pending.ReplaceUpload == nil {
		latest, ok, saveErr := v.cache.RecordPendingReplaceUploadIfUnchanged(pending, pendingReplaceUpload(entry))
		if saveErr != nil {
			finishErr = saveErr.Error()
			logging.L.Warnf("[VFS] upload replace state save failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, saveErr)
			return saveErr
		}
		if !ok {
			finishState = uploadSnapshotStateSuperseded
			if drive.HasCapability(v.driver, drive.CapabilityWriter) && ctx.Err() == nil {
				_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
			}
			return nil
		}
		pending = latest
	}
	if latest, ok := v.cache.PendingByPath(pending.Path); !ok || !samePendingFile(latest, pending) {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed", time.Second, "[VFS] upload committed stale version; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		if drive.HasCapability(v.driver, drive.CapabilityWriter) && ctx.Err() == nil {
			_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
		}
		if ok {
			v.enqueue(latest)
		}
		return nil
	}
	if needsReplace {
		v.setUploadSnapshotState(pending.Path, "replacing_existing")
		phaseStart = timeutil.Now()
		if err := v.replaceUploadedFile(ctx, entry, replaceExisting, pending.Name); err != nil {
			v.recordUploadEvent(pending.Path, "replace_existing", phaseStart, 0, map[string]any{"error": err.Error(), "uploaded_id": entry.ID})
			finishErr = err.Error()
			logging.L.Warnf("[VFS] upload replace existing failed op_id=%q path=%q uploaded_id=%q name=%q err=%v", pending.FID, pending.Path, entry.ID, pending.Name, err)
			if ctx.Err() == nil {
				if latest, ok, saveErr := v.cache.RecordPendingFailure(pending.Path, err, uploadRetryDelay(pending.RetryCount+1, v.uploadDelay)); saveErr != nil {
					logging.L.Warnf("[VFS] upload replace failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
				} else if ok {
					v.enqueue(latest)
				}
			}
			return err
		}
		entry.Name = pending.Name
		v.recordUploadEvent(pending.Path, "replace_existing", phaseStart, 0, map[string]any{"uploaded_id": entry.ID, "replaced": len(replaceExisting)})
	}
	if modTime := v.localModTimeFor(pending.Path); !modTime.IsZero() {
		entry.ModTime = modTime
	} else if modTime := pendingModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		v.setLocalModTime(pending.Path, modTime)
	}
	phaseStart = timeutil.Now()
	v.seedReadCacheFromStaging(entry, snapshot.Path)
	v.recordUploadEvent(pending.Path, "cache_seed", phaseStart, pending.Size, map[string]any{"entry_id": entry.ID})
	phaseStart = timeutil.Now()
	v.mu.Lock()
	v.entries[pending.Path] = entry
	v.unhideCopyChild(filepath.Dir(pending.Path), pending.Name)
	v.invalidateListLocked(filepath.Dir(pending.Path))
	v.mu.Unlock()
	removed, err := v.cache.RemovePendingIfUnchanged(pending)
	pendingCleanupExtra := map[string]any{"removed": removed}
	if err != nil {
		pendingCleanupExtra["error"] = err.Error()
	}
	v.recordUploadEvent(pending.Path, "pending_cleanup", phaseStart, 0, pendingCleanupExtra)
	if err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload committed but pending cleanup failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		return err
	}
	if !removed {
		finishState = uploadSnapshotStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed_after_update", time.Second, "[VFS] upload committed stale version after local update; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		if drive.HasCapability(v.driver, drive.CapabilityWriter) && ctx.Err() == nil {
			_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
		}
		if latest, ok := v.cache.PendingByPath(pending.Path); ok {
			v.enqueue(latest)
		}
		return nil
	}
	phaseStart = timeutil.Now()
	stagingErr := v.cache.staging.remove(pending.LocalPath)
	stagingExtra := map[string]any{}
	if stagingErr != nil {
		stagingExtra["error"] = stagingErr.Error()
	}
	v.recordUploadEvent(pending.Path, "staging_cleanup", phaseStart, 0, stagingExtra)
	finishState = uploadSnapshotStateCompleted
	logging.L.InfofEvery("vfs.upload_complete", time.Second, "[VFS] upload complete op_id=%q path=%q uploaded_id=%q size=%d dur=%s", pending.FID, pending.Path, entry.ID, entry.Size, time.Since(uploadStart))
	return nil
}

type uploadSnapshot struct {
	Path   string
	Hashes drive.SourceHashes
}

func (v *VFS) snapshotPending(pending PendingFile) (uploadSnapshot, error) {
	unlock := v.lockPath(pending.Path)
	defer unlock()
	if err := v.cache.staging.sync(pending.LocalPath); err != nil {
		return uploadSnapshot{}, err
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		return uploadSnapshot{}, err
	}
	if info.Size() != pending.Size {
		return uploadSnapshot{}, fmt.Errorf("vfs: pending changed during upload snapshot: file has %d, expected %d", info.Size(), pending.Size)
	}
	src, err := os.Open(pending.LocalPath)
	if err != nil {
		return uploadSnapshot{}, err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(pending.LocalPath), filepath.Base(pending.LocalPath)+".upload-*")
	if err != nil {
		return uploadSnapshot{}, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	md5Hash := md5.New()
	sha1Hash := sha1.New()
	sha256Hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, md5Hash, sha1Hash, sha256Hash), src)
	if err != nil {
		tmp.Close()
		return uploadSnapshot{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return uploadSnapshot{}, err
	}
	if err := tmp.Close(); err != nil {
		return uploadSnapshot{}, err
	}
	if written != pending.Size {
		return uploadSnapshot{}, fmt.Errorf("vfs: upload snapshot size mismatch: wrote %d, expected %d", written, pending.Size)
	}
	if cleaned := v.cache.staging.cleanupUploadTempsFor(pending.LocalPath, tmpPath); cleaned > 0 {
		logging.L.InfofEvery("vfs.cleanup_old_upload_snapshots", time.Second, "[VFS] cleaned old upload snapshots path=%q local=%q count=%d", pending.Path, pending.LocalPath, cleaned)
	}
	cleanup = false
	return uploadSnapshot{
		Path: tmpPath,
		Hashes: drive.SourceHashes{
			drive.HashMD5:    md5Hash.Sum(nil),
			drive.HashSHA1:   sha1Hash.Sum(nil),
			drive.HashSHA256: sha256Hash.Sum(nil),
		},
	}, nil
}

func (v *VFS) seedReadCacheFromStaging(entry drive.Entry, localPath string) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" || localPath == "" {
		return
	}
	if err := v.cache.PutLocalFile(cacheKey, entry.Size, localPath); err != nil {
		logging.L.Warnf("[VFS] read cache seed failed id=%q local=%q err=%v", entry.ID, localPath, err)
	}
}

type uploadTarget struct {
	UploadName      string
	ReplaceExisting []drive.Entry
	AlreadyReplaced bool
}

func (v *VFS) prepareUploadTarget(ctx context.Context, parentID, name, fid, replaceUploadID string) (uploadTarget, error) {
	target := uploadTarget{UploadName: name}
	if !drive.HasCapability(v.driver, drive.CapabilityWriter) {
		return target, nil
	}
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return target, err
	}
	tempName := temporaryUploadName(name, fid)
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		switch entry.Name {
		case name:
			if replaceUploadID != "" && entry.ID == replaceUploadID {
				target.AlreadyReplaced = true
				continue
			}
			target.ReplaceExisting = append(target.ReplaceExisting, entry)
		case tempName:
			if replaceUploadID != "" && entry.ID == replaceUploadID {
				continue
			}
			logging.L.InfofEvery("vfs.remove_stale_temp_upload", time.Second, "[VFS] removing stale temporary upload parent=%q name=%q id=%q size=%d", parentID, tempName, entry.ID, entry.Size)
			if err := v.driver.Remove(ctx, entry); err != nil {
				return target, err
			}
		}
	}
	if len(target.ReplaceExisting) > 0 {
		target.UploadName = tempName
	}
	return target, nil
}

func (v *VFS) replaceUploadedFile(ctx context.Context, uploaded drive.Entry, existing []drive.Entry, finalName string) error {
	for _, entry := range existing {
		logging.L.InfofEvery("vfs.remove_existing_after_upload", time.Second, "[VFS] removing existing file after replacement upload parent=%q name=%q id=%q size=%d", entry.ParentID, entry.Name, entry.ID, entry.Size)
		if err := v.driver.Remove(ctx, entry); err != nil {
			return err
		}
	}
	if err := v.driver.Rename(ctx, uploaded, finalName); err != nil {
		return err
	}
	return nil
}

func temporaryUploadName(name, fid string) string {
	if fid == "" {
		fid = stagingFID(name)
	}
	return ".qrypt-upload-" + fid + "-" + name
}

func pendingReplaceUploadID(upload *PendingReplaceUpload) string {
	if upload == nil {
		return ""
	}
	return upload.ID
}

func pendingReplaceUpload(entry drive.Entry) PendingReplaceUpload {
	return PendingReplaceUpload{
		ID:       entry.ID,
		ParentID: entry.ParentID,
		Name:     entry.Name,
		Size:     entry.Size,
	}
}

func pendingReplaceUploadEntry(upload PendingReplaceUpload) drive.Entry {
	return drive.Entry{
		ID:       upload.ID,
		ParentID: upload.ParentID,
		Name:     upload.Name,
		Size:     upload.Size,
	}
}

func validateUploadedEntry(entry drive.Entry, name string, size int64) error {
	if entry.ID == "" {
		return fmt.Errorf("vfs: upload returned empty entry id")
	}
	if entry.Size != size {
		return fmt.Errorf("vfs: upload returned size %d, expected %d", entry.Size, size)
	}
	if entry.Name != "" && entry.Name != name {
		return fmt.Errorf("vfs: upload returned name %q, expected %q", entry.Name, name)
	}
	return nil
}

func (v *VFS) enqueue(p PendingFile) {
	if p.PermanentFail {
		logging.L.WarnfEvery("vfs.enqueue_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q size=%d retry=%d last_error=%q", p.FID, p.Path, p.Size, p.RetryCount, p.LastError)
		return
	}
	v.enqueueAfter(p, pendingUploadDelay(p, v.uploadDelay))
}

type debugUploadProgress struct {
	v      *VFS
	path   string
	update func(int64)
}

func (p debugUploadProgress) Phase(phase drive.UploadPhase) {
	if p.v != nil && p.path != "" && phase != "" {
		p.v.setUploadSnapshotState(p.path, string(phase))
	}
}

func (p debugUploadProgress) Uploaded(n int64) {
	if p.update != nil {
		p.update(n)
	}
}

func (v *VFS) enqueueAfter(p PendingFile, delay time.Duration) {
	if delay > 0 {
		v.scheduleUpload(p, delay)
		return
	}
	v.sendUpload(p)
}

func (v *VFS) scheduleUpload(p PendingFile, delay time.Duration) {
	v.uploadMu.Lock()
	if timer := v.uploadTimers[p.Path]; timer != nil {
		timer.Stop()
		logging.L.DebugfEvery("vfs.reschedule_upload", time.Second, "[VFS] reschedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	} else {
		logging.L.DebugfEvery("vfs.schedule_upload", time.Second, "[VFS] schedule upload op_id=%q path=%q size=%d delay=%s", p.FID, p.Path, p.Size, delay)
	}
	v.uploadTimers[p.Path] = time.AfterFunc(delay, func() {
		v.uploadMu.Lock()
		delete(v.uploadTimers, p.Path)
		v.uploadMu.Unlock()
		v.sendUpload(p)
	})
	v.uploadMu.Unlock()
}

func (v *VFS) cancelUpload(path string) {
	path = cleanVirtual(path)
	v.uploadMu.Lock()
	if timer := v.uploadTimers[path]; timer != nil {
		timer.Stop()
		delete(v.uploadTimers, path)
	}
	v.uploadMu.Unlock()
}

func (v *VFS) cancelChildUploads(dir string) {
	dir = cleanVirtual(dir)
	v.uploadMu.Lock()
	for path, timer := range v.uploadTimers {
		if path == dir || isPathUnder(path, dir) {
			timer.Stop()
			delete(v.uploadTimers, path)
		}
	}
	v.uploadMu.Unlock()
}

func (v *VFS) stopUploadTimers() {
	v.uploadMu.Lock()
	defer v.uploadMu.Unlock()
	for path, timer := range v.uploadTimers {
		timer.Stop()
		delete(v.uploadTimers, path)
	}
}

func (v *VFS) sendUpload(p PendingFile) {
	select {
	case v.queue <- p:
		logging.L.DebugfEvery("vfs.upload_enqueued", time.Second, "[VFS] upload enqueued op_id=%q path=%q size=%d retry=%d", p.FID, p.Path, p.Size, p.RetryCount)
	default:
		logging.L.Warnf("[VFS] upload queue full; blocking enqueue in background op_id=%q path=%q size=%d", p.FID, p.Path, p.Size)
		go func() { v.queue <- p }()
	}
}

func pendingUploadDelay(p PendingFile, fallback time.Duration) time.Duration {
	if p.NextAttemptAt <= 0 {
		return fallback
	}
	next := time.Unix(0, p.NextAttemptAt)
	if delay := time.Until(next); delay > 0 {
		return delay
	}
	return 0
}

func (v *VFS) pendingQuietDelay(p PendingFile) time.Duration {
	quietWindow := v.pendingQuietWindow(p)
	if quietWindow <= 0 || p.UpdatedAt <= 0 {
		return 0
	}
	quietFor := time.Since(time.Unix(0, p.UpdatedAt))
	if quietFor >= quietWindow {
		return 0
	}
	return quietWindow - quietFor
}

func (v *VFS) pendingQuietWindow(p PendingFile) time.Duration {
	quietWindow := v.uploadDelay
	if p.Size >= largeUploadQuietThreshold && quietWindow < largeUploadQuietDelay {
		quietWindow = largeUploadQuietDelay
	}
	return quietWindow
}

type uploadAdmission struct {
	mu          sync.Mutex
	activeSmall int
	activeLarge bool
}

func (a *uploadAdmission) tryAcquire(p PendingFile, workers int) bool {
	if workers <= 0 {
		workers = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		if a.activeLarge || a.activeSmall > 0 {
			return false
		}
		a.activeLarge = true
		return true
	}
	if a.activeLarge || a.activeSmall >= workers {
		return false
	}
	a.activeSmall++
	return true
}

func (a *uploadAdmission) release(p PendingFile) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if isLargeUpload(p) {
		a.activeLarge = false
		return
	}
	if a.activeSmall > 0 {
		a.activeSmall--
	}
}

func isLargeUpload(p PendingFile) bool {
	return p.Size >= largeUploadQuietThreshold
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
