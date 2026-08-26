package vfs

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/read"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
	"github.com/yinzhenyu/qrypt/pkg/vfs/view"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Debug runtime adapters: implementations of the diagnostics domain
// interfaces over VFS internals (health, cache, read history, resolve,
// snapshot, staging, upload snapshots).

type vfsDebugCacheRuntime struct {
	st    *readState
	store *uploadStore
}

func newVFSDebugCacheRuntime(st *readState, store *uploadStore) vfsDebugCacheRuntime {
	return vfsDebugCacheRuntime{st: st, store: store}
}

func (r vfsDebugCacheRuntime) ReadCache() readcache.DebugReadCache {
	return r.st.DebugSnapshot()
}

func (r vfsDebugCacheRuntime) Journal() *upload.DebugJournal {
	return r.store.DebugJournal()
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
// --- fault injection (registry lives in pkg/vfs/faultinject) ---
type vfsDebugHealthRuntime struct {
	tracker *drive.HealthTracker
	driver  drive.Driver
}

func newVFSDebugHealthRuntime(tracker *drive.HealthTracker, driver drive.Driver) vfsDebugHealthRuntime {
	return vfsDebugHealthRuntime{tracker: tracker, driver: driver}
}

func (r vfsDebugHealthRuntime) Status() drive.HealthTrackerStatus {
	return r.tracker.Status()
}

func (r vfsDebugHealthRuntime) DriverMetrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return r.driver.Metrics(ctx, since)
}

type vfsDebugReadRuntime struct {
	st *readState
}

func newVFSDebugReadRuntime(st *readState) vfsDebugReadRuntime {
	return vfsDebugReadRuntime{st: st}
}

func (r vfsDebugReadRuntime) NextOpID() string {
	return fmt.Sprintf("read-%d", r.st.NextSequence())
}

func (r vfsDebugReadRuntime) CacheCounters() (hits, misses int64) {
	if r.st.Cache() == nil {
		return 0, 0
	}
	return r.st.Cache().Counters()
}

func (r vfsDebugReadRuntime) AppendEvent(event drive.MetricEvent) {
	r.st.AppendHistory(event)
}

func (r vfsDebugReadRuntime) History() []drive.MetricEvent {
	return r.st.HistorySnapshot()
}

func (r vfsDebugReadRuntime) ResetHistory() {
	r.st.ResetHistory()
}

func (r vfsDebugResolveRuntime) Resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.resolver.resolve(ctx, path)
}

type vfsDebugResolveRuntime struct {
	resolver        pathResolver
	store           *uploadStore
	view            *view.View
	rootID          string
	driver          drive.Driver
	uploadSnapshots func([]PendingUpload) []uploadSnapshot
}

func newVFSDebugResolveRuntime(v *VFS) vfsDebugResolveRuntime {
	return vfsDebugResolveRuntime{
		resolver:        v,
		store:           v.uploads.Store(),
		view:            v.view,
		rootID:          v.rootID,
		driver:          v.driver,
		uploadSnapshots: v.uploadSnapshots,
	}
}

func (r vfsDebugResolveRuntime) PendingUpload(path string) (PendingUpload, bool) {
	pending, ok := r.store.UploadByPath(vfstypes.CleanVirtualPath(path))
	return pending, ok
}

func (r vfsDebugResolveRuntime) PendingUploadByRemoteID(remoteID string) (PendingUpload, bool) {
	for _, pending := range r.store.PendingUploads() {
		if pending.FID == remoteID {
			return pending, true
		}
	}
	return PendingUpload{}, false
}

func (r vfsDebugResolveRuntime) PathByRemoteID(remoteID string) (string, bool) {
	var foundPath string
	var found bool
	r.view.RangeEntries(func(path string, entry drive.Entry) bool {
		if entry.ID == remoteID {
			foundPath = path
			found = true
			return false
		}
		return true
	})
	return foundPath, found
}

func (r vfsDebugResolveRuntime) CacheID(entry drive.Entry) string {
	return read.CacheKey(r.rootID, entry)
}

func (r vfsDebugResolveRuntime) Encrypted() bool {
	return diagnostics.DriverMarkedEncrypted(r.driver)
}

func (r vfsDebugResolveRuntime) DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool) {
	snapshot, err := r.driver.DebugSnapshot(ctx)
	return snapshot, err == nil
}

func (r vfsDebugResolveRuntime) ResolveRemoteName(ctx context.Context, plainName string) (string, bool) {
	if !drive.HasCapability(r.driver, drive.CapabilityRemoteNameResolver) {
		return "", false
	}
	nameInfo, err := r.driver.ResolveRemoteName(ctx, plainName)
	if err != nil {
		return "", false
	}
	return nameInfo.RemoteName, true
}

func (r vfsDebugResolveRuntime) RemoteList(ctx context.Context, parentID string) ([]drive.Entry, error) {
	return r.driver.List(ctx, parentID)
}

func (r vfsDebugResolveRuntime) ForeignEntries(ctx context.Context, parentID string) ([]drive.ForeignEntry, error) {
	if !drive.HasCapability(r.driver, drive.CapabilityForeignEntries) {
		return nil, nil
	}
	entries, err := r.driver.ForeignEntries(ctx, parentID)
	if err != nil {
		return nil, nil
	}
	return entries, nil
}

func (r vfsDebugResolveRuntime) UploadInProgress(path string) bool {
	for _, snap := range r.uploadSnapshots(r.store.PendingUploads()) {
		if snap.Path == path && snap.State == upload.SnapshotStateUploading {
			return true
		}
	}
	return false
}

type vfsDebugSnapshotRuntime struct {
	driverRT              vfsDriverRuntime
	rootID                string
	uploads               *uploadService
	deletes               *DeleteService
	view                  *view.View
	read                  *readState
	debugCacheSnapshot    func() diagnostics.DebugCacheSnapshot
	debugReadHistory      func() []drive.MetricEvent
	uploadSnapshots       func([]PendingUpload) []uploadSnapshot
	uploadSnapshotHistory func() []uploadSnapshot
	debugHotChunks        func() (int, int64)
}

func newVFSDebugSnapshotRuntime(v *VFS) vfsDebugSnapshotRuntime {
	return vfsDebugSnapshotRuntime{
		driverRT:              newVFSDriverRuntime(v.driver, v.testEnabled),
		rootID:                v.rootID,
		uploads:               v.uploads,
		deletes:               v.deletes,
		view:                  v.view,
		read:                  v.read,
		debugCacheSnapshot:    v.debugCacheSnapshot,
		debugReadHistory:      v.debugReadHistory,
		uploadSnapshots:       v.uploadSnapshots,
		uploadSnapshotHistory: v.uploadSnapshotHistory,
		debugHotChunks:        v.debugHotChunks,
	}
}

func (r vfsDebugSnapshotRuntime) Identity(name string) diagnostics.MountSnapshotIdentity {
	return diagnostics.MountSnapshotIdentity{
		Name:         name,
		RootID:       r.rootID,
		Capabilities: r.driverRT.Capabilities(),
		Encrypted:    r.driverRT.Encrypted(),
	}
}

func (r vfsDebugSnapshotRuntime) Queues() diagnostics.MountSnapshotQueues {
	return diagnostics.MountSnapshotQueues{
		UploadLength:  len(r.uploads.Queue()),
		UploadCap:     cap(r.uploads.Queue()),
		UploadWorkers: r.uploads.WorkerCount(),
		UploadDelay:   r.uploads.DefaultDelay().String(),
		DeleteDelay:   r.deletes.delay.String(),
	}
}

func (r vfsDebugSnapshotRuntime) PendingUploads() []PendingUpload {
	return r.uploads.Store().PendingUploads()
}

func (r vfsDebugSnapshotRuntime) DriverSnapshot(ctx context.Context) (drive.DebugSnapshot, bool) {
	snapshot, err := r.driverRT.DebugSnapshot(ctx)
	return snapshot, err == nil
}

func (r vfsDebugSnapshotRuntime) DriverMetrics(ctx context.Context, since time.Time) []drive.MetricEvent {
	metrics, err := r.driverRT.Metrics(ctx, since)
	if err != nil {
		return nil
	}
	return metrics
}

func (r vfsDebugSnapshotRuntime) UploadTimers() []diagnostics.DebugTimer {
	deadlines := r.uploads.ScheduledDeadlines()
	timers := make([]diagnostics.DebugTimer, 0, len(deadlines))
	for path, deadline := range deadlines {
		timers = append(timers, diagnostics.DebugTimer{Path: path, Deadline: deadline})
	}
	sort.Slice(timers, func(i, j int) bool {
		return timers[i].Path < timers[j].Path
	})
	return timers
}

func (r vfsDebugSnapshotRuntime) Overlay() diagnostics.OverlaySnapshot {
	snap := r.view.Overlay().Snapshot(r.deletes.tasks)
	out := diagnostics.OverlaySnapshot{}
	for _, path := range snap.DeleteTimers {
		out.DeleteTimers = append(out.DeleteTimers, diagnostics.DebugTimer{Path: path})
	}
	for _, d := range snap.Deleted {
		out.Deleted = append(out.Deleted, diagnostics.DebugDeletedEntry{
			Path: d.Path, ID: d.ID, Name: d.Name, IsDir: d.IsDir, Size: d.Size,
		})
	}
	for _, op := range snap.RenameOps {
		out.OverlayOps = append(out.OverlayOps, diagnostics.DebugOverlayOp{
			OldPath: op.OldPath, NewPath: op.NewPath, EntryID: op.EntryID, IsDir: op.IsDir, OldGone: op.OldGone, NewSeen: op.NewSeen,
		})
	}
	for _, d := range snap.RestoredDirs {
		out.RestoredDirs = append(out.RestoredDirs, diagnostics.DebugTimer{Path: d.Path, Deadline: d.Deadline})
	}
	for _, c := range snap.CopyHidden {
		item := diagnostics.DebugCopyHidden{Dir: c.Dir}
		for _, n := range c.Names {
			item.Names = append(item.Names, diagnostics.DebugTimer{Path: n.Path, Deadline: n.Deadline})
		}
		if len(item.Names) > 0 {
			out.CopyHidden = append(out.CopyHidden, item)
		}
	}
	return out
}

func (r vfsDebugSnapshotRuntime) Cache() diagnostics.DebugCacheSnapshot {
	return r.debugCacheSnapshot()
}

func (r vfsDebugSnapshotRuntime) ReadHistory() []drive.MetricEvent {
	return r.debugReadHistory()
}

func (r vfsDebugSnapshotRuntime) UploadSnapshots(pending []PendingUpload) []uploadSnapshot {
	return r.uploadSnapshots(pending)
}

func (r vfsDebugSnapshotRuntime) UploadHistory() []uploadSnapshot {
	return r.uploadSnapshotHistory()
}

func (r vfsDebugSnapshotRuntime) StartedAt() time.Time {
	return DebugStartedAt()
}

func (r vfsDebugSnapshotRuntime) Runtime() diagnostics.RuntimeSnapshot {
	out := diagnostics.RuntimeSnapshot{
		HotChunkLimit: read.HotChunkLimit,
		RangeHitLimit: read.RangeHitLimit,
	}
	out.WindowLoads, out.Prefetches, out.RangeHitCount = r.read.RuntimeStats()
	out.HotChunkCount, out.HotChunkBytes = r.debugHotChunks()
	return out
}

type vfsDebugStagingRuntime struct {
	store           *uploadStore
	uploadSnapshots func([]PendingUpload) []uploadSnapshot
}

func newVFSDebugStagingRuntime(v *VFS) vfsDebugStagingRuntime {
	return vfsDebugStagingRuntime{
		store:           v.uploads.Store(),
		uploadSnapshots: v.uploadSnapshots,
	}
}

func (r vfsDebugStagingRuntime) PendingUploads() []PendingUpload {
	return r.store.PendingUploads()
}

func (r vfsDebugStagingRuntime) UploadingPaths(pending []PendingUpload) map[string]bool {
	uploading := map[string]bool{}
	for _, snap := range r.uploadSnapshots(pending) {
		if snap.State == upload.SnapshotStateUploading {
			uploading[snap.Path] = true
		}
	}
	return uploading
}

func (r vfsDebugStagingRuntime) StagingDir() string {
	return r.store.StagingDir()
}

func (r vfsDebugStagingRuntime) StagingFiles() ([]diagnostics.DebugStagingFile, error) {
	entries, err := os.ReadDir(r.StagingDir())
	if err != nil {
		return nil, err
	}
	files := make([]diagnostics.DebugStagingFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".staging") {
			continue
		}
		localPath := filepath.Join(r.StagingDir(), entry.Name())
		info, statErr := entry.Info()
		file := diagnostics.DebugStagingFile{LocalPath: localPath, Exists: statErr == nil}
		if statErr != nil {
			file.Issue = statErr.Error()
		} else {
			file.StagingSize = info.Size()
			modTime := info.ModTime()
			file.ModTime = &modTime
		}
		files = append(files, file)
	}
	return files, nil
}
