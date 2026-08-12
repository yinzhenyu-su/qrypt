package control

import (
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/faultinject"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

type HealthResponse struct {
	API             string                `json:"api"`
	OK              bool                  `json:"ok"`
	Timestamp       time.Time             `json:"timestamp"`
	PID             int                   `json:"pid"`
	DebugStarted    time.Time             `json:"debug_started_at"`
	GoVersion       string                `json:"go_version"`
	NumGoroutine    int                   `json:"num_goroutine"`
	ListenAddress   string                `json:"listen_address,omitempty"`
	TaskPersistence TaskPersistenceStatus `json:"task_persistence"`
}

type TaskPersistenceStatus struct {
	Degraded bool   `json:"degraded"`
	Error    string `json:"error,omitempty"`
}

type PendingResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Pending       []vfs.PendingUpload `json:"pending"`
}

type UploadsResponse struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   time.Time               `json:"generated_at"`
	Path          string                  `json:"path,omitempty"`
	History       bool                    `json:"history"`
	Uploads       []upload.UploadSnapshot `json:"uploads"`
}

type ReadsResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	GeneratedAt   time.Time           `json:"generated_at"`
	Path          string              `json:"path,omitempty"`
	Reads         []drive.MetricEvent `json:"reads"`
}

type DriversResponse struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   time.Time            `json:"generated_at"`
	Drivers       []DebugDriverSummary `json:"drivers"`
}

type MountHealthResponse struct {
	SchemaVersion int                       `json:"schema_version"`
	GeneratedAt   time.Time                 `json:"generated_at"`
	Mounts        []diagnostics.MountHealth `json:"mounts"`
}

type ActiveOpsResponse struct {
	SchemaVersion int                            `json:"schema_version"`
	GeneratedAt   time.Time                      `json:"generated_at"`
	Mounts        []diagnostics.DebugActiveMount `json:"mounts"`
}

type EventsResponse struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Events        []logging.Event `json:"events"`
}

type DebugResetResponse struct {
	SchemaVersion  int       `json:"schema_version"`
	GeneratedAt    time.Time `json:"generated_at"`
	DebugStartedAt time.Time `json:"debug_started_at"`
	Reset          []string  `json:"reset"`
}

type DebugDriverSummary struct {
	Mount        string              `json:"mount"`
	Capabilities []drive.Capability  `json:"capabilities,omitempty"`
	Driver       drive.DebugSnapshot `json:"driver"`
	Metrics      []drive.MetricEvent `json:"metrics,omitempty"`
	Space        *DebugSpaceSummary  `json:"space,omitempty"`
}

type DebugSpaceSummary struct {
	BytesTotal    int64  `json:"bytes_total"`
	BytesFree     int64  `json:"bytes_free"`
	Total         string `json:"total"`
	Free          string `json:"free"`
	Unsupported   bool   `json:"unsupported,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
}

type ListResponse struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Path          string      `json:"path"`
	Source        string      `json:"source"`
	Entries       []ListEntry `json:"entries"`
}

type ListEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type ResolveResponse struct {
	SchemaVersion int                            `json:"schema_version"`
	GeneratedAt   time.Time                      `json:"generated_at"`
	Resolve       *diagnostics.DebugResolveInfo  `json:"resolve,omitempty"`
	Resolves      []diagnostics.DebugResolveInfo `json:"resolves,omitempty"`
}

type TransferMountContext struct {
	Name         string             `json:"name"`
	Driver       string             `json:"driver,omitempty"`
	Capabilities []drive.Capability `json:"capabilities,omitempty"`
	Encrypted    bool               `json:"encrypted"`
}

type TransferContextResponse struct {
	SchemaVersion     int                          `json:"schema_version"`
	GeneratedAt       time.Time                    `json:"generated_at"`
	Source            diagnostics.DebugResolveInfo `json:"source"`
	Destination       diagnostics.DebugResolveInfo `json:"destination"`
	DestinationParent diagnostics.DebugResolveInfo `json:"destination_parent"`
	SourceMount       TransferMountContext         `json:"source_mount"`
	DestinationMount  TransferMountContext         `json:"destination_mount"`
	Compatible        bool                         `json:"compatible"`
	Warnings          []string                     `json:"warnings"`
}

type CacheResponse struct {
	SchemaVersion int                           `json:"schema_version"`
	GeneratedAt   time.Time                     `json:"generated_at"`
	Path          string                        `json:"path,omitempty"`
	Resolve       *diagnostics.DebugResolveInfo `json:"resolve,omitempty"`
	Mounts        []DebugCacheMountStatus       `json:"mounts"`
}

type StagingResponse struct {
	SchemaVersion int                             `json:"schema_version"`
	GeneratedAt   time.Time                       `json:"generated_at"`
	Path          string                          `json:"path,omitempty"`
	Mounts        []diagnostics.DebugStagingMount `json:"mounts"`
}

type DebugCacheMountStatus struct {
	Mount string                         `json:"mount"`
	Cache diagnostics.DebugCacheSnapshot `json:"cache"`
}

type ConsistencyResponse struct {
	SchemaVersion int                             `json:"schema_version"`
	GeneratedAt   time.Time                       `json:"generated_at"`
	Report        diagnostics.ConsistencyReport   `json:"report,omitempty"`
	Reports       []diagnostics.ConsistencyReport `json:"reports,omitempty"`
}

type RuntimeResponse struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   time.Time     `json:"generated_at"`
	GoVersion     string        `json:"go_version"`
	GOOS          string        `json:"goos"`
	GOARCH        string        `json:"goarch"`
	NumCPU        int           `json:"num_cpu"`
	NumGoroutine  int           `json:"num_goroutine"`
	Process       ProcessMemory `json:"process"`
	Mem           MemStats      `json:"mem"`
}

type ProcessMemory struct {
	PID          int    `json:"pid"`
	RSSBytes     uint64 `json:"rss_bytes,omitempty"`
	RSSAvailable bool   `json:"rss_available"`
	RSSSource    string `json:"rss_source,omitempty"`
}

type DebugUploadCancelFaultsResponse struct {
	SchemaVersion int                                  `json:"schema_version"`
	GeneratedAt   time.Time                            `json:"generated_at"`
	Faults        []faultinject.DebugUploadCancelFault `json:"faults"`
}

type MemStats struct {
	Alloc      uint64 `json:"alloc"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapSys    uint64 `json:"heap_sys"`
	NumGC      uint32 `json:"num_gc"`
}

type UploadMemoryResponse struct {
	SchemaVersion int                      `json:"schema_version"`
	GeneratedAt   time.Time                `json:"generated_at"`
	Runtime       RuntimeResponse          `json:"runtime"`
	Uploads       []upload.UploadSnapshot  `json:"uploads"`
	Drivers       []UploadMemoryDriver     `json:"drivers,omitempty"`
	Diagnostics   []UploadMemoryDiagnostic `json:"diagnostics,omitempty"`
}

type UploadMemoryDriver struct {
	Mount         string              `json:"mount"`
	Driver        string              `json:"driver,omitempty"`
	ActiveUploads int                 `json:"active_uploads,omitempty"`
	RecentParts   []drive.MetricEvent `json:"recent_parts,omitempty"`
}

type UploadMemoryDiagnostic struct {
	Level   string         `json:"level"`
	Code    string         `json:"code"`
	Mount   string         `json:"mount,omitempty"`
	Message string         `json:"message"`
	Extra   map[string]any `json:"extra,omitempty"`
}

type ReadMemoryResponse struct {
	SchemaVersion int                    `json:"schema_version"`
	GeneratedAt   time.Time              `json:"generated_at"`
	Runtime       RuntimeResponse        `json:"runtime"`
	Mounts        []ReadMemoryMount      `json:"mounts"`
	Diagnostics   []ReadMemoryDiagnostic `json:"diagnostics,omitempty"`
}

type ReadMemoryMount struct {
	Mount       string                           `json:"mount"`
	Driver      string                           `json:"driver,omitempty"`
	Runtime     diagnostics.MountSnapshotRuntime `json:"runtime"`
	Cache       diagnostics.DebugCacheSnapshot   `json:"cache"`
	PhaseCounts map[string]int                   `json:"phase_counts,omitempty"`
	RecentReads []drive.MetricEvent              `json:"recent_reads,omitempty"`
}

type ReadMemoryDiagnostic struct {
	Level   string         `json:"level"`
	Code    string         `json:"code"`
	Mount   string         `json:"mount,omitempty"`
	Message string         `json:"message"`
	Extra   map[string]any `json:"extra,omitempty"`
}
