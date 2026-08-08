package vfs

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Type aliases — the implementations live in internal/upload.
type UploadService = upload.Service
type uploadStore = upload.PendingStore
type uploadTaskRecord = upload.UploadTaskRecord
type UploadSnapshot = upload.UploadSnapshot
type uploadSnapshotState = upload.SnapshotState
type uploadScheduleState = upload.ScheduleState
type uploadDebugState = upload.DebugState
type uploadFaultState = upload.FaultState
type debugUploadCancelFault = upload.CancelFault
type DebugUploadCancelFault = upload.DebugUploadCancelFault
type uploadAdmission = upload.Admission
type journalEntry = upload.JournalEntry
type DebugJournal = upload.DebugJournal
type DebugJournalPath = upload.DebugJournalPath

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
func newUploadService(store *uploadStore, opts Options, done chan struct{}, hashes *uploadHashTrackerState) *UploadService {
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

var sameUploadRecord = upload.SameUploadRecord
var sameUploadReplacement = upload.SameUploadReplacement

var ReplayUploadJournal = upload.ReplayUploadJournal
var PruneUploadJournal = upload.PruneUploadJournal

var largeUploadQuietThreshold int64 = upload.LargeUploadQuietThreshold
var largeUploadQuietDelay = upload.LargeUploadQuietDelay

// VFS wrapper methods delegating to UploadService. These exist for
// backward compatibility with internal callers; the upload schedule
// implementation lives in internal/upload.

func (v *VFS) enqueue(p PendingUpload)                         { v.uploads.Enqueue(p) }
func (v *VFS) enqueueAfter(p PendingUpload, d time.Duration)   { v.uploads.EnqueueAfter(p, d) }
func (v *VFS) cancelUpload(path string)                        { v.uploads.CancelUpload(path) }
func (v *VFS) cancelChildUploads(dir string)                   { v.uploads.CancelChildUploads(dir) }
func (v *VFS) uploadQuietDelay(p PendingUpload) time.Duration  { return v.uploads.QuietDelay(p) }
func (v *VFS) uploadQuietWindow(p PendingUpload) time.Duration { return v.uploads.QuietWindow(p) }

var _ = upload.Service{} // keep import used
