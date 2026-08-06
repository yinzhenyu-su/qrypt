package vfs

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
