package vfs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
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

func (v *VFS) beginDebugActive(op DebugActiveOp) string {
	if op.OpID == "" {
		op.OpID = fmt.Sprintf("active-%d", atomic.AddUint64(&v.activeSequence, 1))
	}
	if op.Mount == "" {
		op.Mount = v.name
	}
	if op.State == "" {
		op.State = "active"
	}
	now := timeutil.Now()
	op.StartedAt = now
	op.UpdatedAt = now
	v.activeMu.Lock()
	v.activeOps[op.OpID] = cloneDebugActiveOp(op)
	v.activeMu.Unlock()
	return op.OpID
}

func (v *VFS) updateDebugActive(opID string, fn func(*DebugActiveOp)) {
	if opID == "" {
		return
	}
	v.activeMu.Lock()
	op, ok := v.activeOps[opID]
	if ok {
		fn(&op)
		op.UpdatedAt = timeutil.Now()
		v.activeOps[opID] = cloneDebugActiveOp(op)
	}
	v.activeMu.Unlock()
}

func (v *VFS) finishDebugActive(opID string) {
	if opID == "" {
		return
	}
	v.activeMu.Lock()
	delete(v.activeOps, opID)
	v.activeMu.Unlock()
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
	now := timeutil.Now()
	v.activeMu.Lock()
	ops := make([]DebugActiveOp, 0, len(v.activeOps))
	for _, op := range v.activeOps {
		item := cloneDebugActiveOp(op)
		item.AgeMS = durationMillis(now.Sub(item.StartedAt))
		ops = append(ops, item)
	}
	v.activeMu.Unlock()
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
