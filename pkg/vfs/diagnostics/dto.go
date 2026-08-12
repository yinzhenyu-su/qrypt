// Package diagnostics owns the read-only debug snapshot DTOs and the
// cross-domain aggregation logic for the VFS debug layer: resolve,
// consistency, snapshot, staging and cache diagnostics. It never holds
// runtime state (observe owns active-op tracking, upload owns fault
// injection) and never depends on pkg/vfs.
package diagnostics

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// DebugSnapshotSchemaVersion is bumped when the snapshot JSON shape
// changes incompatibly.
const DebugSnapshotSchemaVersion = 2

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

type MountSnapshot struct {
	Identity    MountSnapshotIdentity `json:"identity"`
	Queues      MountSnapshotQueues   `json:"queues"`
	Overlay     MountSnapshotOverlay  `json:"overlay"`
	UploadState MountSnapshotUploads  `json:"upload_state"`
	Cache       DebugCacheSnapshot    `json:"cache"`
	Events      MountSnapshotEvents   `json:"events"`
	Runtime     MountSnapshotRuntime  `json:"runtime"`
}

// DebugCacheSnapshot aggregates the readcache snapshot with the upload
// journal. readcache.DebugReadCache is embedded so the JSON shape stays
// flat (identical to the pre-split output); the composition lives in the
// diagnostics domain, never inside readcache.
type DebugCacheSnapshot struct {
	readcache.DebugReadCache
	Journal *upload.DebugJournal `json:"journal,omitempty"`
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
	Pending      []vfstypes.PendingUpload `json:"pending,omitempty"`
	Deleted      []DebugDeletedEntry      `json:"deleted,omitempty"`
	OverlayOps   []DebugOverlayOp         `json:"overlay_ops,omitempty"`
	RestoredDirs []DebugTimer             `json:"restored_dirs,omitempty"`
	CopyHidden   []DebugCopyHidden        `json:"copy_hidden,omitempty"`
}

type MountSnapshotUploads struct {
	Active  []upload.UploadSnapshot `json:"active,omitempty"`
	History []upload.UploadSnapshot `json:"history,omitempty"`
}

type MountSnapshotEvents struct {
	Reads  []drive.MetricEvent `json:"reads,omitempty"`
	Driver []drive.MetricEvent `json:"driver,omitempty"`
}

type MountSnapshotRuntime struct {
	WindowLoads   int   `json:"window_loads"`
	Prefetches    int   `json:"prefetches"`
	HotChunkCount int   `json:"hot_chunk_count"`
	HotChunkBytes int64 `json:"hot_chunk_bytes"`
	HotChunkLimit int   `json:"hot_chunk_limit"`
	RangeHitCount int   `json:"range_hit_count"`
	RangeHitLimit int   `json:"range_hit_limit"`
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

type DebugActiveMount struct {
	Mount string                   `json:"mount"`
	Ops   []vfstypes.DebugActiveOp `json:"ops,omitempty"`
}

type DebugActiveProvider interface {
	DebugActiveOps(ctx context.Context, mountNames []string) ([]DebugActiveMount, error)
}

func (s MountSnapshot) ActiveUploads() []upload.UploadSnapshot {
	return s.UploadState.Active
}

func (s MountSnapshot) PendingUploads() []vfstypes.PendingUpload {
	return s.Overlay.Pending
}

func (s MountSnapshot) ActiveDeleteTimers() []DebugTimer {
	return s.Queues.DeleteTimers
}

func (s MountSnapshot) HistoricalUploads() []upload.UploadSnapshot {
	return s.UploadState.History
}

func (s MountSnapshot) ReadEvents() []drive.MetricEvent {
	return s.Events.Reads
}

func (s MountSnapshot) DriverMetricEvents() []drive.MetricEvent {
	return s.Events.Driver
}

func (s MountSnapshot) ReadCacheState() readcache.DebugReadCache {
	return s.Cache.DebugReadCache
}
