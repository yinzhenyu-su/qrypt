package vfs

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Resume checks persisted staging before scheduling frozen uploads. Mutable
// generations remain local until the next explicit flush.
func (v *VFS) Resume(ctx context.Context) {
	for _, pending := range v.uploads.Store().PendingUploads() {
		if info, err := os.Stat(pending.LocalPath); err != nil && pending.Frozen {
			v.failFrozenStaging(pending, fmt.Sprintf("vfs: staged content is unavailable: %v", err))
			continue
		} else if err == nil && info.Size() != pending.Size {
			oldSize := pending.Size
			if pending.Frozen {
				// Never adopt a changed length for a durably flushed generation.
				// Keep its bytes for explicit recovery rather than uploading a
				// truncated or partially reconstructed file.
				v.failFrozenStaging(pending, fmt.Sprintf("vfs: staged content size %d does not match recorded size %d", info.Size(), oldSize))
				continue
			}
			pending.Staging = nil
			pending.Size = info.Size()
			pending.SourceHashes = nil
			pending.UpdatedAt = util.Now().UnixNano()
			if err := v.uploads.Store().SaveUpload(pending); err != nil {
				logging.L.Warnf("[VFS] repair pending staging size failed op_id=%q path=%q old_size=%d staging_size=%d err=%v", pending.FID, pending.Path, oldSize, pending.Size, err)
			} else {
				logging.L.InfofEvery("vfs.repair_pending_staging_size", time.Second, "[VFS] repaired pending staging size op_id=%q path=%q old_size=%d staging_size=%d", pending.FID, pending.Path, oldSize, pending.Size)
			}
		}
		if pending.PermanentFail {
			logging.L.WarnfEvery("vfs.resume_pending_permanent_failure", time.Second, "[VFS] skip permanently failed upload op_id=%q path=%q name=%q size=%d local=%q retry=%d last_error=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount, pending.LastError)
			continue
		}
		if !pending.Frozen {
			logging.L.InfofEvery("vfs.resume_pending_mutable", time.Second, "[VFS] keep unflushed pending local; waiting for next flush op_id=%q path=%q name=%q size=%d local=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath)
			continue
		}
		logging.L.InfofEvery("vfs.resume_pending", time.Second, "[VFS] resume pending upload op_id=%q path=%q name=%q size=%d local=%q retry=%d last_error=%q", pending.FID, pending.Path, pending.Name, pending.Size, pending.LocalPath, pending.RetryCount, pending.LastError)
		v.enqueue(pending)
	}
}

// failFrozenStaging leaves the damaged generation available for explicit
// recovery while preventing it from being scheduled for upload.
func (v *VFS) failFrozenStaging(pending PendingUpload, reason string) {
	pending.Staging = nil
	pending.PermanentFail = true
	pending.LastError = reason
	pending.UpdatedAt = util.Now().UnixNano()
	if err := v.uploads.Store().SaveUpload(pending); err != nil {
		logging.L.Warnf("[VFS] record frozen staging failure failed op_id=%q path=%q local=%q reason=%q save_err=%v", pending.FID, pending.Path, pending.LocalPath, reason, err)
	} else {
		logging.L.Warnf("[VFS] frozen staging unavailable op_id=%q path=%q local=%q reason=%q; left for explicit recovery", pending.FID, pending.Path, pending.LocalPath, reason)
	}
}
