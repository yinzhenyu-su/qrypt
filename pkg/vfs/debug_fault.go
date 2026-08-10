package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type DebugUploadCancelInjector interface {
	DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error)
	DebugClearUploadCancel(ctx context.Context, id string) error
	DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault
}

type DebugUploadCancelRequest struct {
	Path       string            `json:"path,omitempty"`
	OpID       string            `json:"op_id,omitempty"`
	Phase      drive.UploadPhase `json:"phase,omitempty"`
	AfterBytes int64             `json:"after_bytes,omitempty"`
	AfterDelay time.Duration     `json:"after_delay,omitempty"`
	// Once controls one-shot behavior; nil (or omitted) defaults to TRUE
	// for compatibility with clients that never set it.
	Once   *bool         `json:"once,omitempty"`
	Reason string        `json:"reason,omitempty"`
	TTL    time.Duration `json:"ttl,omitempty"`
}

type DebugUploadCancelResult struct {
	ID      string `json:"id"`
	Armed   bool   `json:"armed"`
	Matched string `json:"matched,omitempty"`
}

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return DebugUploadCancelResult{}, ctx.Err()
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
		return DebugUploadCancelResult{}, err
	}
	return DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return v.faults.Clear(id)
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
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
