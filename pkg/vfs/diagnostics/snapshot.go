package diagnostics

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// OverlaySnapshot is the raw overlay state a mount snapshot is assembled
// from (consumer side; pkg/vfs reads it off its overlay).
type OverlaySnapshot struct {
	DeleteTimers []DebugTimer
	Deleted      []DebugDeletedEntry
	OverlayOps   []DebugOverlayOp
	RestoredDirs []DebugTimer
	CopyHidden   []DebugCopyHidden
}

// RuntimeSnapshot is the raw read-runtime counters a mount snapshot is
// assembled from.
type RuntimeSnapshot struct {
	WindowLoads   int
	Prefetches    int
	HotChunkCount int
	HotChunkBytes int64
	RangeHitCount int
	HotChunkLimit int
	RangeHitLimit int
}

// SnapshotRuntime is the full-mount snapshot surface (consumer side).
// pkg/vfs implements it with thin adapters over its stores/state; the
// aggregation below lives here.
type SnapshotRuntime interface {
	Identity(name string) MountSnapshotIdentity
	Queues() MountSnapshotQueues
	PendingUploads() []vfstypes.PendingUpload
	DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool)
	DriverMetrics(ctx context.Context, since time.Time) []drive.MetricEvent
	UploadTimers() []DebugTimer
	Overlay() OverlaySnapshot
	Runtime() RuntimeSnapshot
	Cache() DebugCacheSnapshot
	ReadHistory() []drive.MetricEvent
	UploadSnapshots(pending []vfstypes.PendingUpload) []upload.UploadSnapshot
	UploadHistory() []upload.UploadSnapshot
	StartedAt() time.Time
}

// AssembleMountSnapshot assembles one mount's full snapshot: identity,
// queues, overlay, upload state, cache, events and read-runtime counters,
// with mount/driver decoration applied to events and uploads.
func AssembleMountSnapshot(name string, runtime SnapshotRuntime) MountSnapshot {
	snapshot := MountSnapshot{
		Identity: runtime.Identity(name),
		Queues:   runtime.Queues(),
		Overlay: MountSnapshotOverlay{
			Pending: runtime.PendingUploads(),
		},
		Cache: runtime.Cache(),
		Events: MountSnapshotEvents{
			Reads: runtime.ReadHistory(),
		},
	}
	snapshot.UploadState.Active = runtime.UploadSnapshots(snapshot.Overlay.Pending)
	snapshot.UploadState.History = runtime.UploadHistory()
	if driverSnapshot, ok := runtime.DriverSnapshot(context.Background()); ok {
		snapshot.Identity.Driver = &driverSnapshot
		snapshot.Identity.DriverName = driverSnapshot.Driver
		if DriverEncrypted(driverSnapshot) {
			snapshot.Identity.Encrypted = true
		}
	}
	snapshot.Events.Driver = runtime.DriverMetrics(context.Background(), runtime.StartedAt())
	for i := range snapshot.Events.Reads {
		snapshot.Events.Reads[i].Mount = name
		snapshot.Events.Reads[i].Driver = snapshot.Identity.DriverName
	}
	decorateUpload := func(upload *upload.UploadSnapshot) {
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

// Process describes the process owning the snapshot.
func Process(pid int, startedAt time.Time) DebugProcess {
	return DebugProcess{PID: pid, StartedAt: startedAt}
}
