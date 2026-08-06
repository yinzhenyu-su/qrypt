package vfs

import (
	"path/filepath"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type vfsUploadRuntime struct {
	v *VFS
}

func newVFSUploadRuntime(v *VFS) vfsUploadRuntime {
	return vfsUploadRuntime{v: v}
}

func (r vfsUploadRuntime) ClearUploadHashes(fid string) {
	r.v.upload.hashes.removeFID(fid)
}

func (r vfsUploadRuntime) RetryDelay(retryCount int) time.Duration {
	return uploadRetryDelay(retryCount, r.v.upload.delay)
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
