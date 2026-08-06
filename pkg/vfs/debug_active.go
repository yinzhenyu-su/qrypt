package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"sort"
	"strings"
	"time"
)

type DebugActiveOp struct {
	OpID        string         `json:"op_id"`
	Kind        string         `json:"kind"`
	Phase       string         `json:"phase,omitempty"`
	State       string         `json:"state"`
	Mount       string         `json:"mount,omitempty"`
	Path        string         `json:"path,omitempty"`
	RemoteID    string         `json:"remote_id,omitempty"`
	Offset      int64          `json:"offset,omitempty"`
	Requested   int64          `json:"requested_bytes,omitempty"`
	ChunkIndex  int64          `json:"chunk_index,omitempty"`
	WindowStart int64          `json:"window_start,omitempty"`
	WindowEnd   int64          `json:"window_end,omitempty"`
	Background  bool           `json:"background,omitempty"`
	WaitFor     string         `json:"wait_for,omitempty"`
	StartedAt   time.Time      `json:"started_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	AgeMS       int64          `json:"age_ms"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type DebugActiveMount struct {
	Mount string          `json:"mount"`
	Ops   []DebugActiveOp `json:"ops,omitempty"`
}

type DebugActiveProvider interface {
	DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error)
}

func (v *VFS) beginDebugActive(op DebugActiveOp) uint64 {
	return newVFSDebugActiveRuntime(v).Begin(op)
}

func (v *VFS) updateDebugActive(opID uint64, fn func(*DebugActiveOp)) {
	newVFSDebugActiveRuntime(v).Update(opID, fn)
}

func (v *VFS) finishDebugActive(opID uint64) {
	newVFSDebugActiveRuntime(v).Finish(opID)
}

func (v *VFS) DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !debugActiveMountAllowed(v.name, mountNames) {
		return nil, nil
	}
	return []DebugActiveMount{{Mount: v.name, Ops: v.debugActiveOps()}}, nil
}

func (v *VFS) debugActiveOps() []DebugActiveOp {
	ops := newVFSDebugActiveRuntime(v).Snapshot()
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].StartedAt.Equal(ops[j].StartedAt) {
			return ops[i].OpID < ops[j].OpID
		}
		return ops[i].StartedAt.Before(ops[j].StartedAt)
	})
	return ops
}

func (n *Namespace) DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error) {
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		if debugActiveMountAllowed(name, mountNames) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	mounts := make([]*VFS, 0, len(names))
	for _, name := range names {
		mounts = append(mounts, n.mounts[name])
	}
	n.mu.RUnlock()

	out := make([]DebugActiveMount, 0, len(mounts))
	for i, mount := range mounts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		out = append(out, DebugActiveMount{Mount: names[i], Ops: mount.debugActiveOps()})
	}
	return out, nil
}

func cloneDebugActiveOp(op DebugActiveOp) DebugActiveOp {
	if op.Extra == nil {
		return op
	}
	extra := make(map[string]any, len(op.Extra))
	for k, v := range op.Extra {
		extra[k] = v
	}
	op.Extra = extra
	return op
}

func debugActiveMountAllowed(mountName string, mountNames []string) bool {
	if len(mountNames) == 0 {
		return true
	}
	mountName = cleanMountName(mountName)
	for _, candidate := range mountNames {
		if cleanMountName(strings.TrimSpace(candidate)) == mountName {
			return true
		}
	}
	return false
}

type vfsDebugActiveRuntime struct {
	v *VFS
}

func newVFSDebugActiveRuntime(v *VFS) vfsDebugActiveRuntime {
	return vfsDebugActiveRuntime{v: v}
}

func (r vfsDebugActiveRuntime) Begin(op DebugActiveOp) uint64 {
	if op.OpID == "" {
		op.OpID = fmt.Sprintf("active-%d", r.v.debug.sequence.Add(1))
	}
	if op.Mount == "" {
		op.Mount = r.v.name
	}
	if op.State == "" {
		op.State = "active"
	}
	now := timeutil.Now()
	op.StartedAt = now
	op.UpdatedAt = now
	// Linear probe for a free slot. Active ops are short-lived, so an empty
	// slot is almost always found on the first try; no per-op allocation and
	// no shared mutex.
	state := r.v.debug
	for i := 0; i < debugActiveSlots; i++ {
		seq := state.sequence.Add(1)
		slot := &state.slots[seq%debugActiveSlots]
		slot.mu.Lock()
		if slot.seq.Load() == 0 {
			slot.op = op
			slot.seq.Store(seq)
			slot.mu.Unlock()
			return seq
		}
		slot.mu.Unlock()
	}
	// All slots busy: tracking is skipped for this op (no data loss for
	// existing ops; the new op is transient).
	return 0
}

func (r vfsDebugActiveRuntime) Update(opID uint64, fn func(*DebugActiveOp)) {
	if opID == 0 {
		return
	}
	slot := &r.v.debug.slots[opID%debugActiveSlots]
	if slot.seq.Load() != opID {
		return
	}
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		fn(&slot.op)
		slot.op.UpdatedAt = timeutil.Now()
	}
	slot.mu.Unlock()
}

func (r vfsDebugActiveRuntime) Finish(opID uint64) {
	if opID == 0 {
		return
	}
	slot := &r.v.debug.slots[opID%debugActiveSlots]
	slot.mu.Lock()
	if slot.seq.Load() == opID {
		slot.op = DebugActiveOp{}
		slot.seq.Store(0)
	}
	slot.mu.Unlock()
}

func (r vfsDebugActiveRuntime) Snapshot() []DebugActiveOp {
	now := timeutil.Now()
	state := r.v.debug
	ops := make([]DebugActiveOp, 0, debugActiveSlots)
	for i := range state.slots {
		slot := &state.slots[i]
		if slot.seq.Load() == 0 {
			continue
		}
		slot.mu.RLock()
		item := cloneDebugActiveOp(slot.op)
		slot.mu.RUnlock()
		item.AgeMS = durationMillis(now.Sub(item.StartedAt))
		ops = append(ops, item)
	}
	return ops
}
