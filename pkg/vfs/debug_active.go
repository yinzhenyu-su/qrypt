package vfs

import (
	"context"
	"sort"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/vfs/vfstypes"
)

func (v *VFS) beginDebugActive(op vfstypes.DebugActiveOp) uint64 {
	return v.activeDebug.Begin(op)
}

func (v *VFS) updateDebugActive(opID uint64, fn func(*DebugActiveOp)) {
	v.activeDebug.Update(opID, fn)
}

func (v *VFS) finishDebugActive(opID uint64) {
	v.activeDebug.Finish(opID)
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
	ops := v.activeDebug.Snapshot()
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].StartedAt.Equal(ops[j].StartedAt) {
			return ops[i].OpID < ops[j].OpID
		}
		return ops[i].StartedAt.Before(ops[j].StartedAt)
	})
	return ops
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
