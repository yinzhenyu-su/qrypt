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
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
	uploadStart := timeutil.Now()
	logging.L.InfofEvery("vfs.upload_start", time.Second, "[VFS] upload start op_id=%q path=%q parent=%q name=%q size=%d local=%q retry=%d", pending.FID, pending.Path, pending.ParentID, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount)
	v.startDebugUpload(pending)
	if pending.UpdatedAt > 0 {
		queuedAt := time.Unix(0, pending.UpdatedAt)
		if uploadStart.After(queuedAt) {
			v.recordDebugUploadTrace(pending.Path, "queue_wait", queuedAt, 0, nil)
		}
	}
	v.setDebugUploadExtra(pending.Path, "local_path", pending.LocalPath)
	v.setDebugUploadExtra(pending.Path, "parent_id", pending.ParentID)
	finishState := debugUploadStateFailed
	finishErr := ""
	defer func() { v.finishDebugUpload(pending.Path, finishState, finishErr) }()
	v.setDebugUploadState(pending.Path, debugUploadStatePreparing)
	phaseStart := timeutil.Now()
	snapshot, err := v.snapshotPending(pending)
	hashNames := []string{string(drive.HashMD5), string(drive.HashSHA1), string(drive.HashSHA256)}
	snapshotExtra := map[string]any{"hashes": hashNames}
	if err != nil {
		snapshotExtra["error"] = err.Error()
	}
	v.setDebugUploadMetadata(pending.Path, "", hashNames)
	v.recordDebugUploadTrace(pending.Path, "snapshot_hash", phaseStart, pending.Size, snapshotExtra)
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
		finishState = debugUploadStateSuperseded
		logging.L.InfofEvery("vfs.upload_superseded_after_snapshot", time.Second, "[VFS] upload superseded after snapshot op_id=%q path=%q old_size=%d new_size=%d", pending.FID, pending.Path, pending.Size, latest.Size)
		v.enqueue(latest)
		return nil
	}
	v.setDebugUploadState(pending.Path, "removing_existing")
	phaseStart = timeutil.Now()
	if err := v.removeExistingFile(ctx, pending.ParentID, pending.Name); err != nil {
		v.recordDebugUploadTrace(pending.Path, "remove_existing", phaseStart, 0, map[string]any{"error": err.Error()})
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload remove existing failed path=%q parent=%q name=%q err=%v", pending.Path, pending.ParentID, pending.Name, err)
		return err
	}
	v.recordDebugUploadTrace(pending.Path, "remove_existing", phaseStart, 0, nil)
	v.setDebugUploadState(pending.Path, debugUploadStateUploading)
	source := drive.NewLocalReadOnlyFileSourceWithHashes(snapshot.Path, pending.Size, snapshot.Hashes)
	progress := debugUploadProgress{
		v:    v,
		path: pending.Path,
		update: func(n int64) {
			v.updateDebugUpload(pending.Path, int(n))
		},
	}
	phaseStart = timeutil.Now()
	entry, err := v.driver.PutSource(ctx, drive.UploadRequest{
		ParentID: pending.ParentID,
		Name:     pending.Name,
		Source:   source,
		Progress: progress,
	})
	v.setDebugUploadMetadata(pending.Path, entry.ID, nil)
	traceExtra := map[string]any{"entry_id": entry.ID}
	if err != nil {
		traceExtra["error"] = err.Error()
	}
	v.recordDebugUploadTrace(pending.Path, "driver_put_source", phaseStart, pending.Size, traceExtra)
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
			} else if latest, ok, saveErr := v.cache.RecordPendingFailure(pending.Path, err, v.uploadDelay); saveErr != nil {
				logging.L.Warnf("[VFS] upload failed and failure state save failed op_id=%q path=%q err=%v save_err=%v", pending.FID, pending.Path, err, saveErr)
			} else if ok {
				logging.L.WarnfEvery("vfs.upload_failed_requeue", time.Second, "[VFS] upload failed; requeue op_id=%q path=%q name=%q size=%d retry=%d next_attempt=%d err=%v", latest.FID, latest.Path, latest.Name, latest.Size, latest.RetryCount, latest.NextAttemptAt, err)
				v.enqueue(latest)
			}
		}
		return err
	}
	if latest, ok := v.cache.PendingByPath(pending.Path); !ok || !samePendingFile(latest, pending) {
		finishState = debugUploadStateSuperseded
		logging.L.InfofEvery("vfs.upload_stale_committed", time.Second, "[VFS] upload committed stale version; removing uploaded replacement op_id=%q path=%q uploaded_id=%q", pending.FID, pending.Path, entry.ID)
		if drive.HasCapability(v.driver, drive.CapabilityWriter) && ctx.Err() == nil {
			_ = v.driver.Remove(context.WithoutCancel(ctx), entry)
		}
		if ok {
			v.enqueue(latest)
		}
		return nil
	}
	if modTime := v.localModTimeFor(pending.Path); !modTime.IsZero() {
		entry.ModTime = modTime
	} else if modTime := pendingModTime(pending); !modTime.IsZero() {
		entry.ModTime = modTime
		v.setLocalModTime(pending.Path, modTime)
	}
	phaseStart = timeutil.Now()
	v.seedReadCacheFromStaging(entry, snapshot.Path)
	v.recordDebugUploadTrace(pending.Path, "cache_seed", phaseStart, pending.Size, map[string]any{"entry_id": entry.ID})
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
	v.recordDebugUploadTrace(pending.Path, "pending_cleanup", phaseStart, 0, pendingCleanupExtra)
	if err != nil {
		finishErr = err.Error()
		logging.L.Warnf("[VFS] upload committed but pending cleanup failed op_id=%q path=%q uploaded_id=%q err=%v", pending.FID, pending.Path, entry.ID, err)
		return err
	}
	if !removed {
		finishState = debugUploadStateSuperseded
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
	v.recordDebugUploadTrace(pending.Path, "staging_cleanup", phaseStart, 0, stagingExtra)
	finishState = debugUploadStateCompleted
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
	if entry.ID == "" || localPath == "" {
		return
	}
	if err := v.cache.PutLocalFile(entry.ID, localPath); err != nil {
		logging.L.Warnf("[VFS] read cache seed failed id=%q local=%q err=%v", entry.ID, localPath, err)
	}
}

func (v *VFS) removeExistingFile(ctx context.Context, parentID, name string) error {
	if !drive.HasCapability(v.driver, drive.CapabilityWriter) {
		return nil
	}
	entries, err := v.driver.List(ctx, parentID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name == name && !entry.IsDir {
			logging.L.InfofEvery("vfs.remove_existing_before_upload", time.Second, "[VFS] removing existing file before upload parent=%q name=%q id=%q size=%d", parentID, name, entry.ID, entry.Size)
			if err := v.driver.Remove(ctx, entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *VFS) enqueue(p PendingFile) {
	if p.PermanentFail {
		logging.L.WarnfEvery("vfs.enqueue_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q size=%d retry=%d last_error=%q", p.FID, p.Path, p.Size, p.RetryCount, p.LastError)
		return
	}
	v.enqueueAfter(p, v.uploadDelay)
}

type debugUploadProgress struct {
	v      *VFS
	path   string
	update func(int64)
}

func (p debugUploadProgress) Phase(phase drive.UploadPhase) {
	if p.v != nil && p.path != "" && phase != "" {
		p.v.setDebugUploadState(p.path, string(phase))
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
