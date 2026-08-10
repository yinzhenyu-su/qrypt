package vfs

import (
	"context"
	"os"
	"sort"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/internal/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/internal/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) DebugSnapshot() DebugSnapshot {
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "vfs",
		Process:       debugProcess(),
		Mounts:        []MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) DebugSnapshotForMounts(mountNames []string) DebugSnapshot {
	if len(mountNames) == 0 {
		return v.DebugSnapshot()
	}
	names := debugMountNameSet(mountNames)
	if !names[v.name] {
		return DebugSnapshot{
			SchemaVersion: DebugSnapshotSchemaVersion,
			GeneratedAt:   timeutil.Now(),
			Kind:          "vfs",
			Process:       debugProcess(),
		}
	}
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "vfs",
		Process:       debugProcess(),
		Mounts:        []MountSnapshot{v.debugMountSnapshot(v.name)},
	}
}

func (v *VFS) debugMountSnapshot(name string) MountSnapshot {
	return diagnostics.AssembleMountSnapshot(name, newVFSDebugSnapshotRuntime(v))
}

func (v *VFS) debugCacheSnapshot() DebugCacheSnapshot {
	return diagnostics.CacheSnapshot(newVFSDebugCacheRuntime(v))
}

func debugProcess() DebugProcess {
	return diagnostics.Process(os.Getpid(), DebugStartedAt())
}

func debugEncrypted(driver drive.Driver) bool {
	marker, ok := driver.(encryptedMarker)
	return ok && marker.Encrypted()
}

func debugMountNameSet(mountNames []string) map[string]bool {
	set := map[string]bool{}
	for _, name := range mountNames {
		name = cleanMountName(name)
		if name != "" {
			set[name] = true
		}
	}
	return set
}

type vfsDebugSnapshotRuntime struct {
	v *VFS
}

func newVFSDebugSnapshotRuntime(v *VFS) vfsDebugSnapshotRuntime {
	return vfsDebugSnapshotRuntime{v: v}
}

func (r vfsDebugSnapshotRuntime) Identity(name string) MountSnapshotIdentity {
	driverRuntime := newVFSDriverRuntime(r.v)
	return MountSnapshotIdentity{
		Name:         name,
		RootID:       r.v.rootID,
		Capabilities: driverRuntime.Capabilities(),
		Encrypted:    driverRuntime.Encrypted(),
	}
}

func (r vfsDebugSnapshotRuntime) Queues() MountSnapshotQueues {
	return MountSnapshotQueues{
		UploadLength:  len(r.v.uploads.Queue()),
		UploadCap:     cap(r.v.uploads.Queue()),
		UploadWorkers: r.v.uploads.WorkerCount(),
		UploadDelay:   r.v.uploads.DefaultDelay().String(),
		DeleteDelay:   r.v.deletes.delay.String(),
	}
}

func (r vfsDebugSnapshotRuntime) PendingUploads() []PendingUpload {
	return r.v.uploads.Store().PendingUploads()
}

func (r vfsDebugSnapshotRuntime) DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool) {
	snapshot, err := newVFSDriverRuntime(r.v).DebugSnapshot(ctx)
	return snapshot, err == nil
}

func (r vfsDebugSnapshotRuntime) DriverMetrics(ctx context.Context, since time.Time) []drive.MetricEvent {
	metrics, err := newVFSDriverRuntime(r.v).Metrics(ctx, since)
	if err != nil {
		return nil
	}
	return metrics
}

func (r vfsDebugSnapshotRuntime) UploadTimers() []DebugTimer {
	deadlines := r.v.uploads.ScheduledDeadlines()
	timers := make([]DebugTimer, 0, len(deadlines))
	for path, deadline := range deadlines {
		timers = append(timers, DebugTimer{Path: path, Deadline: deadline})
	}
	sort.Slice(timers, func(i, j int) bool {
		return timers[i].Path < timers[j].Path
	})
	return timers
}

func (r vfsDebugSnapshotRuntime) Overlay() diagnostics.OverlaySnapshot {
	now := time.Now()
	out := diagnostics.OverlaySnapshot{}
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for path := range r.v.deletes.tasks.scheduler.Keys() {
		out.DeleteTimers = append(out.DeleteTimers, DebugTimer{Path: path})
	}
	for path, entry := range r.v.view.overlay.deleted {
		out.Deleted = append(out.Deleted, DebugDeletedEntry{
			Path:  path,
			ID:    entry.ID,
			Name:  entry.Name,
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	for _, op := range r.v.view.overlay.renameOverlays {
		out.OverlayOps = append(out.OverlayOps, DebugOverlayOp{
			OldPath: op.oldPath,
			NewPath: op.newPath,
			EntryID: op.entryID,
			IsDir:   op.isDir,
			OldGone: op.oldGone,
			NewSeen: op.newSeen,
		})
	}
	for path, deadline := range r.v.view.overlay.restoredDirs {
		if now.After(deadline) {
			continue
		}
		out.RestoredDirs = append(out.RestoredDirs, DebugTimer{Path: path, Deadline: deadline})
	}
	for dir, names := range r.v.view.overlay.copyHiddenChildren {
		item := DebugCopyHidden{Dir: dir}
		for name, deadline := range names {
			if now.After(deadline) {
				continue
			}
			item.Names = append(item.Names, DebugTimer{Path: name, Deadline: deadline})
		}
		sort.Slice(item.Names, func(i, j int) bool {
			return item.Names[i].Path < item.Names[j].Path
		})
		if len(item.Names) > 0 {
			out.CopyHidden = append(out.CopyHidden, item)
		}
	}
	sort.Slice(out.DeleteTimers, func(i, j int) bool {
		return out.DeleteTimers[i].Path < out.DeleteTimers[j].Path
	})
	sort.Slice(out.Deleted, func(i, j int) bool {
		return out.Deleted[i].Path < out.Deleted[j].Path
	})
	sort.Slice(out.OverlayOps, func(i, j int) bool {
		return out.OverlayOps[i].OldPath < out.OverlayOps[j].OldPath
	})
	sort.Slice(out.RestoredDirs, func(i, j int) bool {
		return out.RestoredDirs[i].Path < out.RestoredDirs[j].Path
	})
	sort.Slice(out.CopyHidden, func(i, j int) bool {
		return out.CopyHidden[i].Dir < out.CopyHidden[j].Dir
	})
	return out
}

func (r vfsDebugSnapshotRuntime) Cache() DebugCacheSnapshot {
	return r.v.debugCacheSnapshot()
}

func (r vfsDebugSnapshotRuntime) ReadHistory() []drive.MetricEvent {
	return r.v.debugReadHistory()
}

func (r vfsDebugSnapshotRuntime) UploadSnapshots(pending []PendingUpload) []UploadSnapshot {
	return r.v.uploadSnapshots(pending)
}

func (r vfsDebugSnapshotRuntime) UploadHistory() []UploadSnapshot {
	return r.v.uploadSnapshotHistory()
}

func (r vfsDebugSnapshotRuntime) StartedAt() time.Time {
	return DebugStartedAt()
}

func (r vfsDebugSnapshotRuntime) Runtime() diagnostics.RuntimeSnapshot {
	out := diagnostics.RuntimeSnapshot{
		HotChunkLimit: read.HotChunkLimit,
		RangeHitLimit: read.RangeHitLimit,
	}
	out.WindowLoads, out.Prefetches, out.RangeHitCount = r.v.read.RuntimeStats()
	out.HotChunkCount, out.HotChunkBytes = r.v.debugHotChunks()
	return out
}

// --- migrated from debug_staging.go ---
