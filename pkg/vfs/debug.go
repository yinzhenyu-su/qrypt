package vfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const DebugSnapshotSchemaVersion = 2
const uploadSnapshotHistoryLimit = 100
const debugReadHistoryLimit = 100

var debugStartedAt = time.Now()

const (
	uploadSnapshotStatePreparing  = string(drive.UploadPhasePreparing)
	uploadSnapshotStateUploading  = string(drive.UploadPhaseUploading)
	uploadSnapshotStateCommitting = string(drive.UploadPhaseCommitting)
	uploadSnapshotStateCompleted  = string(drive.UploadPhaseCompleted)
	uploadSnapshotStateFailed     = "failed"
	uploadSnapshotStateSuperseded = "superseded"
)

type DebugSnapshot struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Kind          string          `json:"kind"`
	Process       DebugProcess    `json:"process"`
	Mounts        []MountSnapshot `json:"mounts"`
}

type DebugProcess struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

type encryptedMarker interface {
	Encrypted() bool
}

type MountSnapshot struct {
	Identity    MountSnapshotIdentity `json:"identity"`
	Queues      MountSnapshotQueues   `json:"queues"`
	Overlay     MountSnapshotOverlay  `json:"overlay"`
	UploadState MountSnapshotUploads  `json:"upload_state"`
	Cache       DebugReadCache        `json:"cache"`
	Events      MountSnapshotEvents   `json:"events"`
	Runtime     MountSnapshotRuntime  `json:"runtime"`
}

type MountSnapshotIdentity struct {
	Name         string               `json:"name"`
	DriverName   string               `json:"driver_name,omitempty"`
	RootID       string               `json:"root_id,omitempty"`
	Capabilities []drive.Capability   `json:"capabilities,omitempty"`
	Encrypted    bool                 `json:"encrypted"`
	Driver       *drive.DebugSnapshot `json:"driver,omitempty"`
}

type MountSnapshotQueues struct {
	UploadLength  int          `json:"upload_length"`
	UploadCap     int          `json:"upload_cap"`
	UploadWorkers int          `json:"upload_workers"`
	UploadDelay   string       `json:"upload_delay"`
	DeleteDelay   string       `json:"delete_delay"`
	UploadTimers  []DebugTimer `json:"upload_timers,omitempty"`
	DeleteTimers  []DebugTimer `json:"delete_timers,omitempty"`
}

type MountSnapshotOverlay struct {
	Pending      []PendingFile       `json:"pending,omitempty"`
	Deleted      []DebugDeletedEntry `json:"deleted,omitempty"`
	OverlayOps   []DebugOverlayOp    `json:"overlay_ops,omitempty"`
	RestoredDirs []DebugTimer        `json:"restored_dirs,omitempty"`
	CopyHidden   []DebugCopyHidden   `json:"copy_hidden,omitempty"`
}

type MountSnapshotUploads struct {
	Active  []UploadSnapshot `json:"active,omitempty"`
	History []UploadSnapshot `json:"history,omitempty"`
}

type MountSnapshotEvents struct {
	Reads  []drive.MetricEvent `json:"reads,omitempty"`
	Driver []drive.MetricEvent `json:"driver,omitempty"`
}

type MountSnapshotRuntime struct {
	ChunkLoads  int `json:"chunk_loads"`
	WindowLoads int `json:"window_loads"`
	Prefetches  int `json:"prefetches"`
}

type UploadSnapshot struct {
	OpID           string              `json:"op_id"`
	Mount          string              `json:"mount,omitempty"`
	Driver         string              `json:"driver,omitempty"`
	Path           string              `json:"path"`
	Name           string              `json:"name"`
	State          string              `json:"state"`
	BytesTotal     int64               `json:"bytes_total"`
	BytesUploaded  int64               `json:"bytes_uploaded"`
	StartedAt      time.Time           `json:"started_at,omitempty"`
	UpdatedAt      time.Time           `json:"updated_at,omitempty"`
	RetryCount     int                 `json:"retry_count"`
	LastError      string              `json:"last_error,omitempty"`
	LastAttemptAt  int64               `json:"last_attempt_at,omitempty"`
	NextAttemptAt  int64               `json:"next_attempt_at,omitempty"`
	CompletedAt    time.Time           `json:"completed_at,omitempty"`
	StageDurations map[string]string   `json:"stage_durations,omitempty"`
	Events         []drive.MetricEvent `json:"events,omitempty"`
	Extra          map[string]any      `json:"extra,omitempty"`
	ParentRemoteID string              `json:"parent_remote_id,omitempty"`
	ResultRemoteID string              `json:"result_remote_id,omitempty"`
	Hashes         []string            `json:"hashes,omitempty"`
	Instant        bool                `json:"instant,omitempty"`
	ErrorCategory  string              `json:"error_category,omitempty"`
}

type uploadSnapshotState struct {
	upload         UploadSnapshot
	stageStartedAt time.Time
	stageDurations map[string]time.Duration
}

type DebugTimer struct {
	Path     string    `json:"path"`
	Deadline time.Time `json:"deadline,omitempty"`
}

type DebugDeletedEntry struct {
	Path  string `json:"path"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type DebugOverlayOp struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	EntryID string `json:"entry_id"`
	IsDir   bool   `json:"is_dir"`
	OldGone bool   `json:"old_gone"`
	NewSeen bool   `json:"new_seen"`
}

type DebugCopyHidden struct {
	Dir   string       `json:"dir"`
	Names []DebugTimer `json:"names"`
}

type DebugReadCache struct {
	MaxBytes       int64                `json:"max_bytes"`
	ChunkCount     int                  `json:"chunk_count"`
	Bytes          int64                `json:"bytes"`
	FileCount      int                  `json:"file_count"`
	Hits           int64                `json:"hits"`
	Misses         int64                `json:"misses"`
	Puts           int64                `json:"puts"`
	Evicted        int64                `json:"evicted"`
	LastGetError   string               `json:"last_get_error,omitempty"`
	LastGetErrorAt *time.Time           `json:"last_get_error_at,omitempty"`
	LastPutError   string               `json:"last_put_error,omitempty"`
	LastPutErrorAt *time.Time           `json:"last_put_error_at,omitempty"`
	Files          []DebugReadCacheFile `json:"files,omitempty"`
}

type DebugReadCacheFile struct {
	ID         string `json:"id"`
	ChunkCount int    `json:"chunk_count"`
	Bytes      int64  `json:"bytes"`
}

type DebugStagingReport struct {
	Path   string              `json:"path,omitempty"`
	Mounts []DebugStagingMount `json:"mounts"`
}

type DebugStagingMount struct {
	Mount        string             `json:"mount"`
	PendingCount int                `json:"pending_count"`
	StagingCount int                `json:"staging_count"`
	OrphanCount  int                `json:"orphan_count"`
	Bytes        int64              `json:"bytes"`
	Files        []DebugStagingFile `json:"files,omitempty"`
	Orphans      []DebugStagingFile `json:"orphans,omitempty"`
}

type DebugStagingFile struct {
	Path             string     `json:"path,omitempty"`
	LocalPath        string     `json:"local_path"`
	Pending          bool       `json:"pending"`
	Exists           bool       `json:"exists"`
	PendingSize      int64      `json:"pending_size,omitempty"`
	StagingSize      int64      `json:"staging_size,omitempty"`
	SizeMatches      bool       `json:"size_matches"`
	UploadInProgress bool       `json:"upload_in_progress"`
	LastError        string     `json:"last_error,omitempty"`
	SHA256           string     `json:"sha256,omitempty"`
	ModTime          *time.Time `json:"mod_time,omitempty"`
	Issue            string     `json:"issue,omitempty"`
}

type DebugResolveInfo struct {
	Path       string `json:"path"`
	Parent     string `json:"parent"`
	Mount      string `json:"mount,omitempty"`
	Driver     string `json:"driver,omitempty"`
	Encrypted  bool   `json:"encrypted"`
	CacheID    string `json:"cache_id,omitempty"`
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name,omitempty"`
	RemoteID   string `json:"remote_id,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Pending    bool   `json:"pending"`
}

type ConsistencyReport struct {
	Path             string               `json:"path"`
	Parent           string               `json:"parent"`
	Name             string               `json:"name"`
	Pending          bool                 `json:"pending"`
	RemoteFound      bool                 `json:"remote_found"`
	RemoteID         string               `json:"remote_id,omitempty"`
	RemoteSize       int64                `json:"remote_size,omitempty"`
	ExpectedSize     int64                `json:"expected_size,omitempty"`
	SizeMatches      bool                 `json:"size_matches"`
	UploadInProgress bool                 `json:"upload_in_progress"`
	Status           string               `json:"status"`
	Issue            string               `json:"issue,omitempty"`
	ForeignEntries   []drive.ForeignEntry `json:"foreign_entries,omitempty"`
}

func (v *VFS) DebugSnapshot() DebugSnapshot {
	return DebugSnapshot{
		SchemaVersion: DebugSnapshotSchemaVersion,
		GeneratedAt:   timeutil.Now(),
		Kind:          "vfs",
		Process:       debugProcess(),
		Mounts:        []MountSnapshot{v.debugMountSnapshot("default")},
	}
}

func (v *VFS) debugMountSnapshot(name string) MountSnapshot {
	snapshot := MountSnapshot{
		Identity: MountSnapshotIdentity{
			Name:         name,
			RootID:       v.rootID,
			Capabilities: drive.Capabilities(v.driver),
			Encrypted:    debugEncrypted(v.driver),
		},
		Queues: MountSnapshotQueues{
			UploadLength:  len(v.queue),
			UploadCap:     cap(v.queue),
			UploadWorkers: v.uploadWorkers,
			UploadDelay:   v.uploadDelay.String(),
			DeleteDelay:   v.deleteDelay.String(),
		},
		Overlay: MountSnapshotOverlay{
			Pending: v.cache.Pending(),
		},
		Cache: v.cache.debugReadCache(),
		Events: MountSnapshotEvents{
			Reads: v.debugReadHistory(),
		},
	}
	snapshot.UploadState.Active = v.uploadSnapshots(snapshot.Overlay.Pending)
	snapshot.UploadState.History = v.uploadSnapshotHistory()
	if driverSnapshot, err := v.driver.DebugSnapshot(context.Background()); err == nil {
		snapshot.Identity.Driver = &driverSnapshot
		snapshot.Identity.DriverName = driverSnapshot.Driver
		if debugDriverEncrypted(driverSnapshot) {
			snapshot.Identity.Encrypted = true
		}
	}
	if metrics, err := v.driver.Metrics(context.Background(), debugStartedAt); err == nil {
		snapshot.Events.Driver = metrics
	}
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

	v.uploadMu.Lock()
	for path := range v.uploadTimers {
		snapshot.Queues.UploadTimers = append(snapshot.Queues.UploadTimers, DebugTimer{Path: path})
	}
	v.uploadMu.Unlock()
	sort.Slice(snapshot.Queues.UploadTimers, func(i, j int) bool {
		return snapshot.Queues.UploadTimers[i].Path < snapshot.Queues.UploadTimers[j].Path
	})

	now := time.Now()
	v.deleteMu.Lock()
	for path := range v.deleteTimers {
		snapshot.Queues.DeleteTimers = append(snapshot.Queues.DeleteTimers, DebugTimer{Path: path})
	}
	for path, entry := range v.deleted {
		snapshot.Overlay.Deleted = append(snapshot.Overlay.Deleted, DebugDeletedEntry{
			Path:  path,
			ID:    entry.ID,
			Name:  entry.Name,
			IsDir: entry.IsDir,
			Size:  entry.Size,
		})
	}
	for _, op := range v.overlayOps {
		snapshot.Overlay.OverlayOps = append(snapshot.Overlay.OverlayOps, DebugOverlayOp{
			OldPath: op.oldPath,
			NewPath: op.newPath,
			EntryID: op.entryID,
			IsDir:   op.isDir,
			OldGone: op.oldGone,
			NewSeen: op.newSeen,
		})
	}
	for path, deadline := range v.restoredDirs {
		if now.After(deadline) {
			continue
		}
		snapshot.Overlay.RestoredDirs = append(snapshot.Overlay.RestoredDirs, DebugTimer{Path: path, Deadline: deadline})
	}
	for dir, names := range v.copyHidden {
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
			snapshot.Overlay.CopyHidden = append(snapshot.Overlay.CopyHidden, item)
		}
	}
	v.deleteMu.Unlock()

	sort.Slice(snapshot.Queues.DeleteTimers, func(i, j int) bool {
		return snapshot.Queues.DeleteTimers[i].Path < snapshot.Queues.DeleteTimers[j].Path
	})
	sort.Slice(snapshot.Overlay.Deleted, func(i, j int) bool {
		return snapshot.Overlay.Deleted[i].Path < snapshot.Overlay.Deleted[j].Path
	})
	sort.Slice(snapshot.Overlay.OverlayOps, func(i, j int) bool {
		return snapshot.Overlay.OverlayOps[i].OldPath < snapshot.Overlay.OverlayOps[j].OldPath
	})
	sort.Slice(snapshot.Overlay.RestoredDirs, func(i, j int) bool {
		return snapshot.Overlay.RestoredDirs[i].Path < snapshot.Overlay.RestoredDirs[j].Path
	})
	sort.Slice(snapshot.Overlay.CopyHidden, func(i, j int) bool {
		return snapshot.Overlay.CopyHidden[i].Dir < snapshot.Overlay.CopyHidden[j].Dir
	})

	v.chunkLoadMu.Lock()
	snapshot.Runtime.ChunkLoads = len(v.chunkLoads)
	v.chunkLoadMu.Unlock()
	v.windowLoadMu.Lock()
	snapshot.Runtime.WindowLoads = len(v.windowLoads)
	v.windowLoadMu.Unlock()
	v.prefetchMu.Lock()
	snapshot.Runtime.Prefetches = len(v.prefetching)
	v.prefetchMu.Unlock()

	return snapshot
}

func (s MountSnapshot) ActiveUploads() []UploadSnapshot {
	return s.UploadState.Active
}

func (s MountSnapshot) PendingFiles() []PendingFile {
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
	return DebugProcess{PID: os.Getpid(), StartedAt: debugStartedAt}
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

func (v *VFS) uploadSnapshots(pending []PendingFile) []UploadSnapshot {
	active := map[string]UploadSnapshot{}
	v.uploadMu.Lock()
	for path, state := range v.activeUploads {
		active[path] = state.upload
	}
	timerPaths := map[string]bool{}
	for path := range v.uploadTimers {
		timerPaths[path] = true
	}
	v.uploadMu.Unlock()

	uploads := make([]UploadSnapshot, 0, len(pending)+len(active))
	seen := map[string]bool{}
	for _, item := range pending {
		if upload, ok := active[item.Path]; ok {
			uploads = append(uploads, upload)
			seen[item.Path] = true
			continue
		}
		state := "queued"
		if item.PermanentFail {
			state = "failed"
		} else if timerPaths[item.Path] {
			state = "scheduled"
			if item.LastError != "" && item.NextAttemptAt > timeutil.Now().UnixNano() {
				state = "retry_wait"
			}
		}
		uploads = append(uploads, UploadSnapshot{
			OpID:           item.FID,
			Path:           item.Path,
			Name:           item.Name,
			State:          state,
			BytesTotal:     item.Size,
			RetryCount:     item.RetryCount,
			LastError:      item.LastError,
			LastAttemptAt:  item.LastAttemptAt,
			NextAttemptAt:  item.NextAttemptAt,
			ParentRemoteID: item.ParentID,
		})
		seen[item.Path] = true
	}
	for path, upload := range active {
		if !seen[path] {
			uploads = append(uploads, upload)
		}
	}
	sort.Slice(uploads, func(i, j int) bool {
		return uploads[i].Path < uploads[j].Path
	})
	return uploads
}

func (v *VFS) uploadSnapshotHistory() []UploadSnapshot {
	v.uploadMu.Lock()
	defer v.uploadMu.Unlock()
	out := append([]UploadSnapshot(nil), v.uploadHistory...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Path < out[j].Path
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out
}

func (v *VFS) startUploadSnapshot(p PendingFile) {
	now := timeutil.Now()
	v.uploadMu.Lock()
	v.activeUploads[p.Path] = &uploadSnapshotState{
		stageStartedAt: now,
		upload: UploadSnapshot{
			OpID:           p.FID,
			Path:           p.Path,
			Name:           p.Name,
			State:          "starting",
			BytesTotal:     p.Size,
			StartedAt:      now,
			UpdatedAt:      now,
			RetryCount:     p.RetryCount,
			LastError:      p.LastError,
			LastAttemptAt:  p.LastAttemptAt,
			NextAttemptAt:  p.NextAttemptAt,
			ParentRemoteID: p.ParentID,
		},
	}
	v.uploadMu.Unlock()
}

func (v *VFS) setUploadSnapshotState(path, state string) {
	v.uploadMu.Lock()
	if upload := v.activeUploads[path]; upload != nil {
		upload.recordStageDuration(timeutil.Now())
		upload.upload.State = state
		if state == string(drive.UploadPhaseInstant) {
			upload.upload.Instant = true
		}
		upload.upload.UpdatedAt = upload.stageStartedAt
	}
	v.uploadMu.Unlock()
}

func (v *VFS) finishUploadSnapshot(path, state, lastError string) {
	v.uploadMu.Lock()
	if upload := v.activeUploads[path]; upload != nil {
		now := timeutil.Now()
		upload.recordStageDuration(now)
		upload.upload.State = state
		upload.upload.LastError = lastError
		if lastError != "" {
			upload.upload.ErrorCategory = drive.ErrorCategoryMessage(lastError)
		}
		upload.upload.UpdatedAt = now
		upload.upload.CompletedAt = upload.upload.UpdatedAt
		v.uploadHistory = append(v.uploadHistory, upload.upload)
		if len(v.uploadHistory) > uploadSnapshotHistoryLimit {
			copy(v.uploadHistory, v.uploadHistory[len(v.uploadHistory)-uploadSnapshotHistoryLimit:])
			v.uploadHistory = v.uploadHistory[:uploadSnapshotHistoryLimit]
		}
		delete(v.activeUploads, path)
	}
	v.uploadMu.Unlock()
}

func (v *VFS) setUploadSnapshotMetadata(path, resultRemoteID string, hashes []string) {
	v.uploadMu.Lock()
	if state := v.activeUploads[path]; state != nil {
		if resultRemoteID != "" {
			state.upload.ResultRemoteID = resultRemoteID
		}
		if len(hashes) > 0 {
			state.upload.Hashes = append([]string(nil), hashes...)
		}
		state.upload.UpdatedAt = timeutil.Now()
	}
	v.uploadMu.Unlock()
}

func (s *uploadSnapshotState) recordStageDuration(now time.Time) {
	if s.stageStartedAt.IsZero() || s.upload.State == "" {
		s.stageStartedAt = now
		return
	}
	if s.upload.StageDurations == nil {
		s.upload.StageDurations = map[string]string{}
	}
	if s.stageDurations == nil {
		s.stageDurations = map[string]time.Duration{}
	}
	s.stageDurations[s.upload.State] += now.Sub(s.stageStartedAt)
	s.upload.StageDurations[s.upload.State] = s.stageDurations[s.upload.State].String()
	s.stageStartedAt = now
}

func (v *VFS) updateUploadSnapshot(path string, n int) {
	if n <= 0 {
		return
	}
	v.uploadMu.Lock()
	if state := v.activeUploads[path]; state != nil {
		state.upload.BytesUploaded += int64(n)
		if state.upload.BytesTotal >= 0 && state.upload.BytesUploaded > state.upload.BytesTotal {
			state.upload.BytesUploaded = state.upload.BytesTotal
		}
		state.upload.UpdatedAt = timeutil.Now()
	}
	v.uploadMu.Unlock()
}

func (v *VFS) recordUploadEvent(path, phase string, start time.Time, bytes int64, extra map[string]any) {
	if phase == "" || start.IsZero() {
		return
	}
	finished := timeutil.Now()
	duration := finished.Sub(start)
	event := drive.MetricEvent{
		At:         finished,
		Kind:       "vfs_upload",
		Operation:  "upload",
		Phase:      phase,
		State:      "completed",
		OK:         true,
		Bytes:      bytes,
		Duration:   duration.String(),
		DurationMS: durationMillis(duration),
		StartedAt:  start,
		FinishedAt: finished,
		Extra:      extra,
	}
	if message, ok := extra["error"].(string); ok && message != "" {
		event.State = "failed"
		event.OK = false
		event.Error = message
		event.ErrorCategory = drive.ErrorCategoryMessage(message)
	}
	if bytes > 0 && duration > 0 {
		event.Throughput = int64(float64(bytes) / duration.Seconds())
	}
	v.uploadMu.Lock()
	if state := v.activeUploads[path]; state != nil {
		state.upload.Events = append(state.upload.Events, event)
	}
	v.uploadMu.Unlock()
}

func (v *VFS) debugCacheCounters() (hits, misses int64) {
	v.cache.mu.RLock()
	hits, misses = v.cache.stats.hits, v.cache.stats.misses
	v.cache.mu.RUnlock()
	return hits, misses
}

func (v *VFS) recordDebugRead(opID, path, remoteID string, offset, requested, bytes int64, source string, cacheHits, cacheMisses, chunks int64, started time.Time, err error) {
	finished := timeutil.Now()
	event := drive.MetricEvent{
		At: finished, OpID: opID, Kind: "vfs_read", Operation: "read", Phase: "read", State: "completed", OK: true,
		Path: path, RemoteID: remoteID, Offset: offset, Requested: requested,
		Bytes: bytes, CacheHits: cacheHits, CacheMisses: cacheMisses, Chunks: chunks,
		StartedAt: started, FinishedAt: finished, Duration: finished.Sub(started).String(), DurationMS: durationMillis(finished.Sub(started)),
		Extra: map[string]any{"source": source},
	}
	if bytes > 0 && finished.After(started) {
		event.Throughput = int64(float64(bytes) / finished.Sub(started).Seconds())
	}
	if err != nil {
		event.State = "failed"
		event.OK = false
		event.Error = err.Error()
		event.ErrorCategory = drive.ErrorCategory(err)
	}
	v.readMu.Lock()
	v.readHistory = append(v.readHistory, event)
	if len(v.readHistory) > debugReadHistoryLimit {
		v.readHistory = append([]drive.MetricEvent(nil), v.readHistory[len(v.readHistory)-debugReadHistoryLimit:]...)
	}
	v.readMu.Unlock()
}

func (v *VFS) debugReadHistory() []drive.MetricEvent {
	v.readMu.Lock()
	defer v.readMu.Unlock()
	return append([]drive.MetricEvent(nil), v.readHistory...)
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Millisecond - 1) / time.Millisecond)
}

func (v *VFS) setUploadSnapshotExtra(path string, key string, value any) {
	if key == "" {
		return
	}
	v.uploadMu.Lock()
	if state := v.activeUploads[path]; state != nil {
		if state.upload.Extra == nil {
			state.upload.Extra = map[string]any{}
		}
		state.upload.Extra[key] = value
		state.upload.UpdatedAt = timeutil.Now()
	}
	v.uploadMu.Unlock()
}

func (c *Cache) debugReadCache() DebugReadCache {
	snapshot := DebugReadCache{MaxBytes: c.maxSize}
	c.mu.RLock()
	snapshot.FileCount = len(c.chunks)
	snapshot.Hits = c.stats.hits
	snapshot.Misses = c.stats.misses
	snapshot.Puts = c.stats.puts
	snapshot.Evicted = c.stats.evicted
	snapshot.LastGetError = c.lastGetError
	if !c.lastGetAt.IsZero() {
		at := c.lastGetAt
		snapshot.LastGetErrorAt = &at
	}
	snapshot.LastPutError = c.lastPutError
	if !c.lastPutAt.IsZero() {
		at := c.lastPutAt
		snapshot.LastPutErrorAt = &at
	}
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		file := DebugReadCacheFile{ID: fid}
		for _, chunk := range fc.chunks {
			snapshot.ChunkCount++
			snapshot.Bytes += chunk.size
			file.ChunkCount++
			file.Bytes += chunk.size
		}
		if file.ChunkCount > 0 {
			snapshot.Files = append(snapshot.Files, file)
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	sort.Slice(snapshot.Files, func(i, j int) bool {
		return snapshot.Files[i].ID < snapshot.Files[j].ID
	})
	return snapshot
}

func (v *VFS) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	mount := v.debugStagingMount("default", path)
	report := DebugStagingReport{Mounts: []DebugStagingMount{mount}}
	if path != "" && path != "/" {
		report.Path = path
	}
	return report, nil
}

func (v *VFS) MountHealth(ctx context.Context, mountName string) ([]MountHealth, error) {
	h := MountHealth{Mount: mountName, CheckedAt: timeutil.Now()}
	result := v.healthTracker.Status()
	if metrics, err := v.driver.Metrics(ctx, timeutil.Now().Add(-drive.DefaultHealthWindow)); err == nil {
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
	return []MountHealth{h}, nil
}

func (v *VFS) Drivers() []NamedDriver {
	return []NamedDriver{{Name: v.name, Driver: v.driver}}
}

func (v *VFS) debugStagingMount(name, path string) DebugStagingMount {
	pending := v.cache.Pending()
	pendingByLocal := map[string]PendingFile{}
	var pendingForPath *PendingFile
	for _, item := range pending {
		pendingByLocal[item.LocalPath] = item
		if path != "" && path != "/" && item.Path == path {
			p := item
			pendingForPath = &p
		}
	}
	uploading := map[string]bool{}
	for _, upload := range v.uploadSnapshots(pending) {
		if upload.State == uploadSnapshotStateUploading {
			uploading[upload.Path] = true
		}
	}

	mount := DebugStagingMount{Mount: name, PendingCount: len(pending)}
	entries, err := os.ReadDir(v.cache.staging.dir)
	if err != nil {
		mount.Orphans = append(mount.Orphans, DebugStagingFile{
			LocalPath: v.cache.staging.dir,
			Issue:     err.Error(),
		})
		return mount
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".staging") {
			continue
		}
		localPath := filepath.Join(v.cache.staging.dir, entry.Name())
		info, statErr := entry.Info()
		file := DebugStagingFile{LocalPath: localPath, Exists: statErr == nil}
		if statErr != nil {
			file.Issue = statErr.Error()
		} else {
			file.StagingSize = info.Size()
			file.ModTime = ptrTime(info.ModTime())
			mount.Bytes += info.Size()
		}
		mount.StagingCount++
		if item, ok := pendingByLocal[localPath]; ok {
			file = mergePendingStagingFile(file, item, uploading[item.Path], path != "" && path != "/" && item.Path == path)
			if path == "" || path == "/" || item.Path == path {
				mount.Files = append(mount.Files, file)
			}
			continue
		}
		file.Pending = false
		file.Issue = "not_referenced_by_pending"
		mount.OrphanCount++
		mount.Orphans = append(mount.Orphans, file)
	}
	if pendingForPath != nil {
		found := false
		for _, file := range mount.Files {
			if file.Path == pendingForPath.Path {
				found = true
				break
			}
		}
		if !found {
			mount.Files = append(mount.Files, pendingStagingFile(*pendingForPath, uploading[pendingForPath.Path], true))
		}
	} else if path != "" && path != "/" {
		mount.Files = nil
	}
	sort.Slice(mount.Files, func(i, j int) bool { return mount.Files[i].Path < mount.Files[j].Path })
	sort.Slice(mount.Orphans, func(i, j int) bool { return mount.Orphans[i].LocalPath < mount.Orphans[j].LocalPath })
	return mount
}

func mergePendingStagingFile(file DebugStagingFile, pending PendingFile, uploading, includeHash bool) DebugStagingFile {
	file.Path = pending.Path
	file.Pending = true
	file.PendingSize = pending.Size
	file.SizeMatches = file.Exists && file.StagingSize == pending.Size
	file.UploadInProgress = uploading
	file.LastError = pending.LastError
	if includeHash && file.Exists {
		if sum, err := fileSHA256(file.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

func pendingStagingFile(pending PendingFile, uploading, includeHash bool) DebugStagingFile {
	file := DebugStagingFile{
		Path:             pending.Path,
		LocalPath:        pending.LocalPath,
		Pending:          true,
		PendingSize:      pending.Size,
		UploadInProgress: uploading,
		LastError:        pending.LastError,
	}
	info, err := os.Stat(pending.LocalPath)
	if err != nil {
		file.Issue = err.Error()
		return file
	}
	file.Exists = true
	file.StagingSize = info.Size()
	file.SizeMatches = file.StagingSize == pending.Size
	file.ModTime = ptrTime(info.ModTime())
	if includeHash {
		if sum, err := fileSHA256(pending.LocalPath); err == nil {
			file.SHA256 = sum
		} else {
			file.Issue = err.Error()
		}
	}
	return file
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func (v *VFS) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	path = cleanVirtual(path)
	info := DebugResolveInfo{
		Path:      path,
		Parent:    filepath.Dir(path),
		PlainName: filepath.Base(path),
	}
	if pending, err := v.pending(path); err == nil {
		info.Pending = true
		info.ParentID = pending.ParentID
		info.RemoteID = pending.FID
		info.Size = pending.Size
	}
	if entry, err := v.resolve(ctx, path); err == nil {
		info.RemoteID = entry.ID
		info.ParentID = entry.ParentID
		info.IsDir = entry.IsDir
		info.Size = entry.Size
	}
	if info.RemoteID != "" {
		info.CacheID = cacheFileID(info.RemoteID)
	}
	info.Encrypted = debugEncrypted(v.driver)
	if driverSnapshot, err := v.driver.DebugSnapshot(ctx); err == nil {
		info.Driver = driverSnapshot.Driver
		if debugDriverEncrypted(driverSnapshot) {
			info.Encrypted = true
		}
	}
	if includeRemoteName {
		if drive.HasCapability(v.driver, drive.CapabilityRemoteNameResolver) {
			nameInfo, err := v.driver.ResolveRemoteName(ctx, info.PlainName)
			if err == nil {
				info.RemoteName = nameInfo.RemoteName
			}
		} else {
			info.RemoteName = info.PlainName
		}
	}
	return info, nil
}

func (v *VFS) DebugResolveByRemoteID(ctx context.Context, remoteID string) (DebugResolveInfo, error) {
	// Search pending files for matching remote ID.
	for _, p := range v.cache.Pending() {
		if p.FID == remoteID {
			return v.DebugResolve(ctx, p.Path, false)
		}
	}
	// Search cached entries for matching remote ID.
	v.mu.RLock()
	defer v.mu.RUnlock()
	for path, entry := range v.entries {
		if entry.ID == remoteID {
			return v.DebugResolve(ctx, path, false)
		}
	}
	return DebugResolveInfo{}, fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
}

func (v *VFS) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	path = cleanVirtual(path)
	report := ConsistencyReport{Path: path, Parent: filepath.Dir(path), Name: filepath.Base(path)}
	expectedKnown := false
	if pending, err := v.pending(path); err == nil {
		report.Pending = true
		report.ExpectedSize = pending.Size
		expectedKnown = true
	}
	parent, err := v.resolve(ctx, report.Parent)
	if err != nil {
		report.Status = "error"
		report.Issue = err.Error()
		return report, nil
	}
	entries, err := v.driver.List(ctx, parent.ID)
	if err != nil {
		return ConsistencyReport{}, err
	}
	if drive.HasCapability(v.driver, drive.CapabilityForeignEntries) {
		foreign, err := v.driver.ForeignEntries(ctx, parent.ID)
		if err != nil {
			return ConsistencyReport{}, err
		}
		report.ForeignEntries = foreign
	}
	for _, entry := range entries {
		if entry.Name == report.Name {
			report.RemoteFound = true
			report.RemoteID = entry.ID
			report.RemoteSize = entry.Size
			if !expectedKnown {
				report.ExpectedSize = entry.Size
			}
			report.SizeMatches = entry.Size == report.ExpectedSize
			break
		}
	}
	for _, upload := range v.uploadSnapshots(v.cache.Pending()) {
		if upload.Path == path && upload.State == uploadSnapshotStateUploading {
			report.UploadInProgress = true
			break
		}
	}
	switch {
	case report.Pending && report.RemoteFound && report.SizeMatches:
		report.Status = "uploaded_pending_cleanup"
	case report.Pending && report.RemoteFound && !report.SizeMatches:
		report.Status = "mismatch"
		report.Issue = "remote size differs from pending size"
	case report.Pending && !report.RemoteFound:
		report.Status = "pending"
	case !report.Pending && report.RemoteFound:
		report.Status = "ok"
		report.SizeMatches = true
	default:
		report.Status = "missing"
		report.Issue = "not pending and not found remotely"
	}
	return report, nil
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

func (n *Namespace) DebugResolve(ctx context.Context, path string, includeRemoteName bool) (DebugResolveInfo, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	if root {
		return DebugResolveInfo{Path: "/", Parent: "/", PlainName: "/", IsDir: true}, nil
	}
	info, err := mount.DebugResolve(ctx, rest, includeRemoteName)
	if err != nil {
		return DebugResolveInfo{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	info.Path = joinVirtual("/"+mountName, strings.TrimPrefix(info.Path, "/"))
	info.Parent = filepath.Dir(info.Path)
	info.Mount = mountName
	return info, nil
}

func (n *Namespace) DebugResolveByRemoteID(ctx context.Context, remoteID string) (*DebugResolveInfo, string, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for name, vfs := range n.mounts {
		info, err := vfs.DebugResolveByRemoteID(ctx, remoteID)
		if err == nil {
			info.Mount = name
			info.Path = joinVirtual("/"+name, strings.TrimPrefix(info.Path, "/"))
			info.Parent = filepath.Dir(info.Path)
			return &info, name, nil
		}
	}
	return nil, "", fmt.Errorf("vfs: no path found for remote ID %q", remoteID)
}

func (n *Namespace) DebugStaging(ctx context.Context, path string) (DebugStagingReport, error) {
	path = cleanVirtual(path)
	report := DebugStagingReport{}
	if path != "/" {
		mount, rest, root, err := n.resolve(path)
		if err != nil {
			return DebugStagingReport{}, err
		}
		if root {
			return DebugStagingReport{Path: path}, nil
		}
		mountName := strings.Trim(strings.TrimPrefix(path, "/"), "/")
		if idx := strings.Index(mountName, "/"); idx >= 0 {
			mountName = mountName[:idx]
		}
		item := mount.debugStagingMount(mountName, rest)
		prefixStagingMountPaths(&item, mountName)
		report.Path = path
		report.Mounts = []DebugStagingMount{item}
		return report, nil
	}

	n.mu.RLock()
	names := make([]string, 0, len(n.mounts))
	for name := range n.mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		item := n.mounts[name].debugStagingMount(name, "/")
		prefixStagingMountPaths(&item, name)
		report.Mounts = append(report.Mounts, item)
	}
	n.mu.RUnlock()
	return report, nil
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
		result = append(result, NamedDriver{Name: name, Driver: n.mounts[name].driver})
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

func prefixStagingMountPaths(mount *DebugStagingMount, mountName string) {
	for i := range mount.Files {
		if mount.Files[i].Path != "" {
			mount.Files[i].Path = joinVirtual("/"+mountName, strings.TrimPrefix(mount.Files[i].Path, "/"))
		}
	}
}

func (n *Namespace) DebugConsistency(ctx context.Context, path string) (ConsistencyReport, error) {
	mount, rest, root, err := n.resolve(path)
	if err != nil {
		return ConsistencyReport{}, err
	}
	if root {
		return ConsistencyReport{Path: "/", Status: "namespace_root"}, nil
	}
	report, err := mount.DebugConsistency(ctx, rest)
	if err != nil {
		return ConsistencyReport{}, err
	}
	mountName := strings.Trim(strings.TrimPrefix(cleanVirtual(path), "/"), "/")
	if idx := strings.Index(mountName, "/"); idx >= 0 {
		mountName = mountName[:idx]
	}
	report.Path = joinVirtual("/"+mountName, strings.TrimPrefix(report.Path, "/"))
	report.Parent = filepath.Dir(report.Path)
	return report, nil
}
