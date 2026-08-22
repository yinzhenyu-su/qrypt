package vfs

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Internal aliases keep the VFS adaptation concise without exporting upload
// runtime implementation types from the public filesystem package.
type uploadService = upload.Service
type uploadStore = upload.PendingStore
type uploadTaskRecord = upload.UploadTaskRecord
type uploadSnapshot = upload.UploadSnapshot
type uploadSnapshotState = upload.SnapshotState
type uploadAdmission = upload.Admission
type uploadTargetIndex = upload.TargetIndex

func newUploadTargetIndex() *uploadTargetIndex {
	return upload.NewTargetIndex(upload.DefaultTargetIndexTTL)
}

// vfsHashOps adapts the vfs hash tracker to upload.HashOps.
type vfsHashOps struct{ h *uploadHashTrackerState }

func (w vfsHashOps) RemovePath(path string) {
	w.h.removePath(path)
}
func (w vfsHashOps) RemoveUnder(path string) {
	w.h.removeUnder(path)
}
func (w vfsHashOps) RenamePath(oldPath, newPath string, p vfstypes.PendingUpload) {
	w.h.renamePath(oldPath, newPath, PendingUpload(p))
}

// newUploadService builds the upload service wired to VFS state.
func newUploadService(store *uploadStore, opts Options, done chan struct{}, hashes *uploadHashTrackerState) *uploadService {
	return upload.NewService(upload.ServiceOptions{
		UploadDelay:   opts.UploadDelay,
		UploadWorkers: opts.UploadWorkers,
		Store:         store,
		Done:          done,
		HashOps:       vfsHashOps{h: hashes},
	})
}

// timeFromUnixNano converts unix nanos to a time.Time (zero when <= 0).
var timeFromUnixNano = upload.TimeFromUnixNano

// PendingUploads returns all pending uploads known to this VFS.
func (v *VFS) PendingUploads() []PendingUpload {
	return v.uploads.PendingUploads()
}

var largeUploadQuietThreshold int64 = upload.LargeUploadQuietThreshold
var largeUploadQuietDelay = upload.LargeUploadQuietDelay

// VFS wrapper methods delegating to uploadService. These exist for
// backward compatibility with internal callers; the upload schedule
// implementation lives in internal/upload.

func (v *VFS) enqueue(p PendingUpload) {
	if v.uploadSchedulingEnabled() {
		v.uploads.Enqueue(p)
	}
}
func (v *VFS) enqueueAfter(p PendingUpload, d time.Duration) bool {
	if !v.uploadSchedulingEnabled() {
		return false
	}
	v.uploads.EnqueueAfter(p, d)
	return true
}

// uploadSchedulingEnabled keeps construction-time writes durable but idle.
// Start.Resume is the single scheduling entrypoint for pending records created
// before workers exist; otherwise a debounce timer can enqueue a generation
// before Start and Resume can enqueue the same generation again.
func (v *VFS) uploadSchedulingEnabled() bool {
	v.lifecycleMu.Lock()
	defer v.lifecycleMu.Unlock()
	return v.lifecycle == lifecycleRunning
}
func (v *VFS) cancelUpload(path string)                        { v.uploads.CancelUpload(path) }
func (v *VFS) uploadQuietDelay(p PendingUpload) time.Duration  { return v.uploads.QuietDelay(p) }
func (v *VFS) uploadQuietWindow(p PendingUpload) time.Duration { return v.uploads.QuietWindow(p) }
