package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"os"
	"sort"
	"time"
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
	runtime := newVFSDebugSnapshotRuntime(v)
	snapshot := MountSnapshot{
		Identity: runtime.Identity(name),
		Queues:   runtime.Queues(),
		Overlay: MountSnapshotOverlay{
			Pending: runtime.PendingUploads(),
		},
		Cache: v.debugCacheSnapshot(),
		Events: MountSnapshotEvents{
			Reads: v.debugReadHistory(),
		},
	}
	snapshot.UploadState.Active = v.uploadSnapshots(snapshot.Overlay.Pending)
	snapshot.UploadState.History = v.uploadSnapshotHistory()
	if driverSnapshot, ok := runtime.DriverSnapshot(context.Background()); ok {
		snapshot.Identity.Driver = &driverSnapshot
		snapshot.Identity.DriverName = driverSnapshot.Driver
		if debugDriverEncrypted(driverSnapshot) {
			snapshot.Identity.Encrypted = true
		}
	}
	snapshot.Events.Driver = runtime.DriverMetrics(context.Background(), DebugStartedAt())
	for i := range snapshot.Events.Reads {
		snapshot.Events.Reads[i].Mount = name
		snapshot.Events.Reads[i].Driver = snapshot.Identity.DriverName
	}
	decorateUpload := func(upload *UploadSnapshot) {
		upload.Mount = name
		upload.Driver = snapshot.Identity.DriverName
		for i := range upload.Events {
			upload.Events[i].OpID = upload.OpID
			upload.Events[i].Mount = name
			upload.Events[i].Driver = snapshot.Identity.DriverName
			upload.Events[i].Path = upload.Path
		}
	}
	for i := range snapshot.UploadState.Active {
		decorateUpload(&snapshot.UploadState.Active[i])
	}
	for i := range snapshot.UploadState.History {
		decorateUpload(&snapshot.UploadState.History[i])
	}

	snapshot.Queues.UploadTimers = runtime.UploadTimers()
	overlay := runtime.Overlay()
	snapshot.Queues.DeleteTimers = overlay.DeleteTimers
	snapshot.Overlay.Deleted = overlay.Deleted
	snapshot.Overlay.OverlayOps = overlay.OverlayOps
	snapshot.Overlay.RestoredDirs = overlay.RestoredDirs
	snapshot.Overlay.CopyHidden = overlay.CopyHidden
	runtimeState := runtime.Runtime()
	snapshot.Runtime.WindowLoads = runtimeState.WindowLoads
	snapshot.Runtime.Prefetches = runtimeState.Prefetches
	snapshot.Runtime.HotChunkCount = runtimeState.HotChunkCount
	snapshot.Runtime.HotChunkBytes = runtimeState.HotChunkBytes
	snapshot.Runtime.HotChunkLimit = runtimeState.HotChunkLimit
	snapshot.Runtime.RangeHitCount = runtimeState.RangeHitCount
	snapshot.Runtime.RangeHitLimit = runtimeState.RangeHitLimit

	return snapshot
}

func (v *VFS) debugCacheSnapshot() DebugReadCache {
	return debugCacheSnapshotWithRuntime(newVFSDebugCacheRuntime(v))
}

func (s MountSnapshot) ActiveUploads() []UploadSnapshot {
	return s.UploadState.Active
}

func (s MountSnapshot) PendingUploads() []PendingUpload {
	return s.Overlay.Pending
}

func (s MountSnapshot) ActiveDeleteTimers() []DebugTimer {
	return s.Queues.DeleteTimers
}

func (s MountSnapshot) HistoricalUploads() []UploadSnapshot {
	return s.UploadState.History
}

func (s MountSnapshot) ReadEvents() []drive.MetricEvent {
	return s.Events.Reads
}

func (s MountSnapshot) DriverMetricEvents() []drive.MetricEvent {
	return s.Events.Driver
}

func (s MountSnapshot) ReadCacheState() DebugReadCache {
	return s.Cache
}

func debugProcess() DebugProcess {
	return DebugProcess{PID: os.Getpid(), StartedAt: DebugStartedAt()}
}

func debugDriverEncrypted(snapshot drive.DebugSnapshot) bool {
	if snapshot.Extra == nil {
		return false
	}
	encrypted, _ := snapshot.Extra["crypt"].(bool)
	return encrypted
}

func debugEncrypted(driver drive.Driver) bool {
	marker, ok := driver.(encryptedMarker)
	return ok && marker.Encrypted()
}

func (n *Namespace) DebugSnapshot() DebugSnapshot {
	snapshot := DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		snapshot.Mounts = append(snapshot.Mounts, n.mounts[name].debugMountSnapshot(name))
	}
	n.mu.RUnlock()
	return snapshot
}

func (n *Namespace) DebugSnapshotForMounts(mountNames []string) DebugSnapshot {
	if len(mountNames) == 0 {
		return n.DebugSnapshot()
	}
	snapshot := DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "namespace",
		Process:       debugProcess(),
	}
	names := debugMountNameSet(mountNames)
	n.mu.RLock()
	matched := make([]string, 0, len(names))
	for name := range names {
		if _, ok := n.mounts[name]; ok {
			matched = append(matched, name)
		}
	}
	sort.Strings(matched)
	for _, name := range matched {
		snapshot.Mounts = append(snapshot.Mounts, n.mounts[name].debugMountSnapshot(name))
	}
	n.mu.RUnlock()
	return snapshot
}

func (n *Namespace) DebugReset(ctx context.Context) error {
	n.mu.RLock()
	mounts := make([]*VFS, 0, len(n.mounts))
	for _, mount := range n.mounts {
		mounts = append(mounts, mount)
	}
	n.mu.RUnlock()
	for _, mount := range mounts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		newVFSDebugReadRuntime(mount).ResetHistory()
	}
	return nil
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

type debugOverlayRuntimeSnapshot struct {
	DeleteTimers []DebugTimer
	Deleted      []DebugDeletedEntry
	OverlayOps   []DebugOverlayOp
	RestoredDirs []DebugTimer
	CopyHidden   []DebugCopyHidden
}

type debugRuntimeStateSnapshot struct {
	WindowLoads   int
	Prefetches    int
	HotChunkCount int
	HotChunkBytes int64
	RangeHitCount int
	HotChunkLimit int
	RangeHitLimit int
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
		UploadLength:  len(r.v.uploads.queue),
		UploadCap:     cap(r.v.uploads.queue),
		UploadWorkers: r.v.uploads.workers,
		UploadDelay:   r.v.uploads.delay.String(),
		DeleteDelay:   r.v.deletes.delay.String(),
	}
}

func (r vfsDebugSnapshotRuntime) PendingUploads() []PendingUpload {
	return r.v.uploads.store.PendingUploads()
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
	r.v.uploads.schedule.mu.Lock()
	defer r.v.uploads.schedule.mu.Unlock()
	timers := make([]DebugTimer, 0, len(r.v.uploads.schedule.timers))
	for path := range r.v.uploads.schedule.timers {
		timers = append(timers, DebugTimer{Path: path})
	}
	sort.Slice(timers, func(i, j int) bool {
		return timers[i].Path < timers[j].Path
	})
	return timers
}

func (r vfsDebugSnapshotRuntime) Overlay() debugOverlayRuntimeSnapshot {
	now := time.Now()
	out := debugOverlayRuntimeSnapshot{}
	r.v.view.overlay.mu.Lock()
	defer r.v.view.overlay.mu.Unlock()
	for path := range r.v.deletes.tasks.timers {
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

func (r vfsDebugSnapshotRuntime) Runtime() debugRuntimeStateSnapshot {
	out := debugRuntimeStateSnapshot{
		HotChunkLimit: readHotChunkLimit,
		RangeHitLimit: readRangeHitLimit,
	}
	r.v.read.windows.mu.Lock()
	out.WindowLoads = len(r.v.read.windows.loads)
	r.v.read.windows.mu.Unlock()
	r.v.read.prefetch.mu.Lock()
	out.Prefetches = len(r.v.read.prefetch.inFlight)
	r.v.read.prefetch.mu.Unlock()
	out.HotChunkCount, out.HotChunkBytes = r.v.debugHotChunks()
	r.v.read.fastPath.rangeHit.mu.Lock()
	out.RangeHitCount = len(r.v.read.fastPath.rangeHit.hits)
	r.v.read.fastPath.rangeHit.mu.Unlock()
	return out
}
