package vfs

import (
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const DebugSnapshotSchemaVersion = 2
const uploadSnapshotHistoryLimit = 100
const debugReadHistoryLimit = 1000

var debugStartedAt = time.Now()
var debugStartedAtMu sync.RWMutex

func DebugStartedAt() time.Time {
	debugStartedAtMu.RLock()
	defer debugStartedAtMu.RUnlock()
	return debugStartedAt
}

func ResetDebugStartedAt() time.Time {
	debugStartedAtMu.Lock()
	defer debugStartedAtMu.Unlock()
	debugStartedAt = timeutil.Now()
	return debugStartedAt
}

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
	Pending      []PendingUpload     `json:"pending,omitempty"`
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
	WindowLoads   int   `json:"window_loads"`
	Prefetches    int   `json:"prefetches"`
	HotChunkCount int   `json:"hot_chunk_count"`
	HotChunkBytes int64 `json:"hot_chunk_bytes"`
	HotChunkLimit int   `json:"hot_chunk_limit"`
	RangeHitCount int   `json:"range_hit_count"`
	RangeHitLimit int   `json:"range_hit_limit"`
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
	Enabled             bool                 `json:"enabled"`
	MaxBytes            int64                `json:"max_bytes"`
	LargeFileThreshold  int64                `json:"large_file_threshold"`
	ChunkCount          int                  `json:"chunk_count"`
	Bytes               int64                `json:"bytes"`
	LargeFileBytes      int64                `json:"large_file_bytes"`
	SmallFileBytes      int64                `json:"small_file_bytes"`
	FileCount           int                  `json:"file_count"`
	Hits                int64                `json:"hits"`
	Misses              int64                `json:"misses"`
	Puts                int64                `json:"puts"`
	Evicted             int64                `json:"evicted"`
	LastGetError        string               `json:"last_get_error,omitempty"`
	LastGetErrorAt      *time.Time           `json:"last_get_error_at,omitempty"`
	LastPutError        string               `json:"last_put_error,omitempty"`
	LastPutErrorAt      *time.Time           `json:"last_put_error_at,omitempty"`
	WriteQueueLength    int                  `json:"write_queue_len"`
	WriteQueueCap       int                  `json:"write_queue_cap"`
	WriteQueueBytes     int64                `json:"write_queue_bytes"`
	WriteQueueMaxBytes  int64                `json:"write_queue_max_bytes"`
	WriteQueueDropped   int64                `json:"write_queue_dropped"`
	LastWriteMS         int64                `json:"last_write_ms,omitempty"`
	MaxWriteMS          int64                `json:"max_write_ms,omitempty"`
	LastWriteQueueMS    int64                `json:"last_write_queue_ms,omitempty"`
	MaxWriteQueueMS     int64                `json:"max_write_queue_ms,omitempty"`
	IndexDirty          bool                 `json:"index_dirty"`
	IndexFlushScheduled bool                 `json:"index_flush_scheduled"`
	Files               []DebugReadCacheFile `json:"files,omitempty"`
	Journal             *DebugJournal        `json:"journal,omitempty"`
}

type DebugReadCacheFile struct {
	ID         string `json:"id"`
	Size       int64  `json:"size,omitempty"`
	Large      bool   `json:"large,omitempty"`
	ChunkCount int    `json:"chunk_count"`
	Bytes      int64  `json:"bytes"`
}

type DebugJournal struct {
	Path               string             `json:"path"`
	Exists             bool               `json:"exists"`
	Bytes              int64              `json:"bytes,omitempty"`
	Entries            int                `json:"entries,omitempty"`
	InvalidEntries     int                `json:"invalid_entries,omitempty"`
	PendingCount       int                `json:"pending_count"`
	UniquePaths        int                `json:"unique_paths,omitempty"`
	DuplicateEntries   int                `json:"duplicate_entries,omitempty"`
	CompactRecommended bool               `json:"compact_recommended"`
	LargestPaths       []DebugJournalPath `json:"largest_paths,omitempty"`
	Error              string             `json:"error,omitempty"`
}

type DebugJournalPath struct {
	Path             string `json:"path"`
	Entries          int    `json:"entries"`
	LatestSize       int64  `json:"latest_size,omitempty"`
	StagingSize      int64  `json:"staging_size,omitempty"`
	SizeMatches      bool   `json:"size_matches"`
	StagingExists    bool   `json:"staging_exists"`
	LastError        string `json:"last_error,omitempty"`
	DuplicateEntries int    `json:"duplicate_entries,omitempty"`
	LastJournalOp    string `json:"last_journal_op,omitempty"`
	LastJournalLine  int    `json:"last_journal_line,omitempty"`
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
