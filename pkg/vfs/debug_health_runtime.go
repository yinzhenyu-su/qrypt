package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]diagnostics.MountHealth, error) {
	return []diagnostics.MountHealth{diagnostics.AssembleHealth(ctx, mountName, newVFSDebugHealthRuntime(v))}, nil
}

func (v *VFS) Drivers() []diagnostics.NamedDriver {
	return []diagnostics.NamedDriver{newVFSDriverRuntime(v).NamedDriver(v.name)}
}

type vfsDebugHealthRuntime struct {
	v *VFS
}

func newVFSDebugHealthRuntime(v *VFS) vfsDebugHealthRuntime {
	return vfsDebugHealthRuntime{v: v}
}

func (r vfsDebugHealthRuntime) Status() drive.HealthTrackerStatus {
	return r.v.healthTracker.Status()
}

func (r vfsDebugHealthRuntime) DriverMetrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return newVFSDriverRuntime(r.v).Metrics(ctx, since)
}

// --- migrated from debug_read.go ---
