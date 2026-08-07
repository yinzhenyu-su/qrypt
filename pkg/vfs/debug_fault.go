package vfs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const debugUploadCancelDefaultTTL = 10 * time.Minute

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
	Once       bool              `json:"once"`
	Reason     string            `json:"reason,omitempty"`
	TTL        time.Duration     `json:"ttl,omitempty"`
}

type DebugUploadCancelResult struct {
	ID      string `json:"id"`
	Armed   bool   `json:"armed"`
	Matched string `json:"matched,omitempty"`
}

type DebugUploadCancelFault struct {
	ID          string            `json:"id"`
	Path        string            `json:"path,omitempty"`
	OpID        string            `json:"op_id,omitempty"`
	Phase       drive.UploadPhase `json:"phase,omitempty"`
	AfterBytes  int64             `json:"after_bytes,omitempty"`
	AfterDelay  string            `json:"after_delay,omitempty"`
	Once        bool              `json:"once"`
	Reason      string            `json:"reason,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	ExpiresAt   time.Time         `json:"expires_at,omitempty"`
	MatchedPath string            `json:"matched_path,omitempty"`
	Fired       bool              `json:"fired"`
	FiredAt     time.Time         `json:"fired_at,omitempty"`
}

type debugUploadCancelFault struct {
	id          string
	path        string
	opID        string
	phase       drive.UploadPhase
	afterBytes  int64
	afterDelay  time.Duration
	once        bool
	reason      string
	createdAt   time.Time
	expiresAt   time.Time
	matchedPath string
	fired       bool
	firedAt     time.Time
}

var debugUploadCancelID uint64

func (v *VFS) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	select {
	case <-ctx.Done():
		return DebugUploadCancelResult{}, ctx.Err()
	default:
	}
	if req.Path == "" && req.OpID == "" {
		return DebugUploadCancelResult{}, fmt.Errorf("vfs: debug upload cancel requires path or op_id")
	}
	if req.Phase == "" && req.AfterBytes <= 0 && req.AfterDelay <= 0 {
		req.Phase = drive.UploadPhaseUploading
	}
	if req.TTL <= 0 {
		req.TTL = debugUploadCancelDefaultTTL
	}
	now := time.Now()
	id := fmt.Sprintf("upload-cancel-%d", atomic.AddUint64(&debugUploadCancelID, 1))
	fault := &debugUploadCancelFault{
		id:         id,
		path:       cleanVirtual(req.Path),
		opID:       req.OpID,
		phase:      req.Phase,
		afterBytes: req.AfterBytes,
		afterDelay: req.AfterDelay,
		once:       true,
		reason:     req.Reason,
		createdAt:  now,
		expiresAt:  now.Add(req.TTL),
	}
	newVFSDebugUploadFaultRuntime(v).PutCancelFault(fault)
	return DebugUploadCancelResult{ID: id, Armed: true}, nil
}

func (v *VFS) DebugClearUploadCancel(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	newVFSDebugUploadFaultRuntime(v).ClearCancelFault(id)
	return nil
}

func (v *VFS) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	return newVFSDebugUploadFaultRuntime(v).CancelFaults(time.Now())
}

func (n *Namespace) DebugInjectUploadCancel(ctx context.Context, req DebugUploadCancelRequest) (DebugUploadCancelResult, error) {
	if req.Path == "" {
		return DebugUploadCancelResult{}, fmt.Errorf("vfs: namespace debug upload cancel requires path")
	}
	mount, rest, root, err := n.resolve(req.Path)
	if err != nil {
		return DebugUploadCancelResult{}, err
	}
	if root || rest == "/" {
		return DebugUploadCancelResult{}, ErrReadOnly
	}
	req.Path = rest
	return mount.DebugInjectUploadCancel(ctx, req)
}

func (n *Namespace) DebugClearUploadCancel(ctx context.Context, id string) error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, mount := range n.mounts {
		if err := mount.DebugClearUploadCancel(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (n *Namespace) DebugUploadCancelFaults(ctx context.Context) []DebugUploadCancelFault {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var out []DebugUploadCancelFault
	for name, mount := range n.mounts {
		for _, fault := range mount.DebugUploadCancelFaults(ctx) {
			if fault.Path != "" {
				fault.Path = "/" + name + cleanVirtual(fault.Path)
			}
			if fault.MatchedPath != "" {
				fault.MatchedPath = "/" + name + cleanVirtual(fault.MatchedPath)
			}
			out = append(out, fault)
		}
	}
	return out
}

func (v *VFS) matchUploadCancelFault(path, opID string) *debugUploadCancelFault {
	return newVFSDebugUploadFaultRuntime(v).MatchCancelFault(time.Now(), path, opID)
}

func (v *VFS) markUploadCancelFaultFired(id string) {
	newVFSDebugUploadFaultRuntime(v).MarkCancelFaultFired(id, time.Now())
}

func (f *debugUploadCancelFault) snapshot() DebugUploadCancelFault {
	s := DebugUploadCancelFault{
		ID:          f.id,
		Path:        f.path,
		OpID:        f.opID,
		Phase:       f.phase,
		AfterBytes:  f.afterBytes,
		Once:        f.once,
		Reason:      f.reason,
		CreatedAt:   f.createdAt,
		ExpiresAt:   f.expiresAt,
		MatchedPath: f.matchedPath,
		Fired:       f.fired,
		FiredAt:     f.firedAt,
	}
	if f.afterDelay > 0 {
		s.AfterDelay = f.afterDelay.String()
	}
	return s
}

type debugUploadCancelProgress struct {
	inner       drive.UploadProgress
	fault       *debugUploadCancelFault
	cancel      context.CancelFunc
	cancelPath  string
	cancelOpID  string
	v           *VFS
	mu          sync.Mutex
	bytes       int64
	phase       drive.UploadPhase
	timer       *time.Timer
	timerArmed  bool
	cancelFired atomic.Bool
}

func (p *debugUploadCancelProgress) Phase(phase drive.UploadPhase) {
	if p.inner != nil {
		p.inner.Phase(phase)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.maybeCancelLocked()
}

func (p *debugUploadCancelProgress) Uploaded(n int64) {
	if p.inner != nil {
		p.inner.Uploaded(n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > 0 {
		p.bytes += n
	}
	p.maybeCancelLocked()
}

func (p *debugUploadCancelProgress) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
	}
}

func (p *debugUploadCancelProgress) maybeCancelLocked() {
	if p.fault == nil || p.cancelFired.Load() {
		return
	}
	if p.fault.phase != "" && p.phase != p.fault.phase {
		return
	}
	if p.fault.afterBytes > 0 && p.bytes < p.fault.afterBytes {
		return
	}
	if p.fault.afterDelay > 0 {
		if p.timerArmed {
			return
		}
		p.timerArmed = true
		p.timer = time.AfterFunc(p.fault.afterDelay, func() {
			p.fire()
		})
		return
	}
	p.fire()
}

func (p *debugUploadCancelProgress) fire() {
	if !p.cancelFired.CompareAndSwap(false, true) {
		return
	}
	logging.L.Warnf("[VFS] debug upload cancel fired op_id=%q path=%q fault=%q reason=%q", p.cancelOpID, p.cancelPath, p.fault.id, p.fault.reason)
	p.v.markUploadCancelFaultFired(p.fault.id)
	p.cancel()
}

type vfsDebugUploadFaultRuntime struct {
	v *VFS
}

func newVFSDebugUploadFaultRuntime(v *VFS) vfsDebugUploadFaultRuntime {
	return vfsDebugUploadFaultRuntime{v: v}
}

func (r vfsDebugUploadFaultRuntime) PutCancelFault(fault *debugUploadCancelFault) {
	if fault == nil || fault.id == "" {
		return
	}
	r.v.uploads.faults.mu.Lock()
	defer r.v.uploads.faults.mu.Unlock()
	if r.v.uploads.faults.cancelFaults == nil {
		r.v.uploads.faults.cancelFaults = map[string]*debugUploadCancelFault{}
	}
	r.v.uploads.faults.cancelFaults[fault.id] = fault
}

func (r vfsDebugUploadFaultRuntime) ClearCancelFault(id string) {
	r.v.uploads.faults.mu.Lock()
	defer r.v.uploads.faults.mu.Unlock()
	if id == "" {
		r.v.uploads.faults.cancelFaults = map[string]*debugUploadCancelFault{}
		return
	}
	delete(r.v.uploads.faults.cancelFaults, id)
}

func (r vfsDebugUploadFaultRuntime) CancelFaults(now time.Time) []DebugUploadCancelFault {
	r.v.uploads.faults.mu.Lock()
	defer r.v.uploads.faults.mu.Unlock()
	r.pruneExpiredCancelFaultsLocked(now)
	out := make([]DebugUploadCancelFault, 0, len(r.v.uploads.faults.cancelFaults))
	for _, fault := range r.v.uploads.faults.cancelFaults {
		out = append(out, fault.snapshot())
	}
	return out
}

func (r vfsDebugUploadFaultRuntime) MatchCancelFault(now time.Time, path, opID string) *debugUploadCancelFault {
	r.v.uploads.faults.mu.Lock()
	defer r.v.uploads.faults.mu.Unlock()
	r.pruneExpiredCancelFaultsLocked(now)
	for _, fault := range r.v.uploads.faults.cancelFaults {
		if fault.fired && fault.once {
			continue
		}
		if fault.path != "" && fault.path != path {
			continue
		}
		if fault.opID != "" && fault.opID != opID {
			continue
		}
		fault.matchedPath = path
		return fault
	}
	return nil
}

func (r vfsDebugUploadFaultRuntime) MarkCancelFaultFired(id string, now time.Time) {
	if id == "" {
		return
	}
	r.v.uploads.faults.mu.Lock()
	defer r.v.uploads.faults.mu.Unlock()
	fault, ok := r.v.uploads.faults.cancelFaults[id]
	if !ok {
		return
	}
	fault.fired = true
	fault.firedAt = now
	if fault.once {
		delete(r.v.uploads.faults.cancelFaults, id)
	}
}

func (r vfsDebugUploadFaultRuntime) pruneExpiredCancelFaultsLocked(now time.Time) {
	for id, fault := range r.v.uploads.faults.cancelFaults {
		if !fault.expiresAt.IsZero() && now.After(fault.expiresAt) {
			delete(r.v.uploads.faults.cancelFaults, id)
		}
	}
}
