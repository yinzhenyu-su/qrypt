package vfs

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/upload"
)

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
