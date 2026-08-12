package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
)

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req faultinject.DebugUploadCancelRequest) (faultinject.DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return faultinject.DebugUploadCancelResult{}, ctx.Err()
	default:
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		req.Phase = drive.UploadPhaseUploading
	}
	id, err := v.faults.Inject(faultinject.InjectRequest{
		Path:       cleanVirtual(req.Path),
		OpID:       req.OpID,
		Phase:      req.Phase,
		AfterBytes: req.AfterBytes,
		AfterDelay: req.AfterDelay,
		Once:       req.Once == nil || *req.Once,
		Reason:     req.Reason,
		TTL:        req.TTL,
	})
	if err != nil {
		return faultinject.DebugUploadCancelResult{}, err
	}
	return faultinject.DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return v.faults.Clear(id)
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []faultinject.DebugUploadCancelFault {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return v.faults.Faults(time.Now())
}

func (v *VFS) matchUploadCancelFault(path, opID string) (faultinject.MatchResult, bool) {
	return v.faults.Match(time.Now(), path, opID)
}

// --- migrated from debug_health.go ---
