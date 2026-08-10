package vfs

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	return []MountHealth{diagnostics.AssembleHealth(ctx, mountName, newVFSDebugHealthRuntime(v))}, nil
}

func (v *VFS) Drivers() []NamedDriver {
	return []NamedDriver{newVFSDriverRuntime(v).NamedDriver(v.name)}
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
