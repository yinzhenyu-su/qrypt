package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"sort"
)

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	return []MountHealth{newVFSDebugHealthRuntime(v).MountHealth(ctx, mountName)}, nil
}

func (v *VFS) Drivers() []NamedDriver {
	return []NamedDriver{newVFSDriverRuntime(v).NamedDriver(v.name)}
}

func (n *Namespace) Drivers() []NamedDriver {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var result []NamedDriver
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fs := n.mounts[name]
		result = append(result, newVFSDriverRuntime(fs).NamedDriver(name))
	}
	return result
}

func (n *Namespace) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if mountName != "" {
		vfs, ok := n.mounts[cleanMountName(mountName)]
		if !ok {
			return nil, fmt.Errorf("vfs: mount %q not found", mountName)
		}
		return vfs.MountHealth(ctx, mountName)
	}
	var results []MountHealth
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		health, _ := n.mounts[name].MountHealth(ctx, name)
		results = append(results, health...)
	}
	return results, nil
}

type vfsDebugHealthRuntime struct {
	v *VFS
}

func newVFSDebugHealthRuntime(v *VFS) vfsDebugHealthRuntime {
	return vfsDebugHealthRuntime{v: v}
}

func (r vfsDebugHealthRuntime) MountHealth(ctx context.Context, mountName string) MountHealth {
	h := MountHealth{Mount: mountName, CheckedAt: timeutil.Now()}
	result := r.v.healthTracker.Status()
	if metrics, err := newVFSDriverRuntime(r.v).Metrics(ctx, timeutil.Now().Add(-drive.DefaultHealthWindow)); err == nil {
		driverHealth := drive.HealthStatusFromMetrics(metrics, drive.DefaultHealthWindow, drive.DefaultMaxEvents)
		result = drive.MergeHealthStatus(result, driverHealth)
	}
	h.OK = result.OK
	h.Level = result.Level
	h.Error = result.Error
	h.CheckedAt = result.CheckedAt
	h.Success = result.Success
	h.Errors = result.Errors
	if len(result.Ops) > 0 {
		h.Ops = map[string]MountHealthOp{}
		for op, status := range result.Ops {
			h.Ops[op] = MountHealthOp{
				Success:     status.Success,
				Errors:      status.Errors,
				LastError:   status.LastError,
				LastErrorAt: status.LastErrorAt,
			}
		}
	}
	return h
}
