package drive

import (
	"context"
	"time"
)

type DebugSnapshot struct {
	Driver      string         `json:"driver"`
	Health      string         `json:"health"`
	GeneratedAt time.Time      `json:"generated_at"`
	Stats       map[string]any `json:"stats,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type Debugger interface {
	DebugSnapshot(ctx context.Context) (DebugSnapshot, error)
}

const (
	HealthLevelOK        = "ok"
	HealthLevelDegraded  = "degraded"
	HealthLevelUnhealthy = "unhealthy"
)

const (
	DebugExtraInstantUploadCount     = "instant_upload_count"
	DebugExtraLegacyRapidUploadCount = "rapid_upload_count"
	DebugExtraCredentialSource       = "credential_source"
	DebugExtraCredentialUpdated      = "credential_updated"
	DebugExtraLastError              = "last_error"
)

const (
	DebugStatRootID   = "root_id"
	DebugStatRootPath = "root_path"
)

type RemoteNameInfo struct {
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name"`
}

type RemoteNameResolver interface {
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
}

type ForeignEntry struct {
	ID         string `json:"id"`
	ParentID   string `json:"parent_id,omitempty"`
	RemoteName string `json:"remote_name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ForeignEntryLister interface {
	ForeignEntries(ctx context.Context, parentID string) ([]ForeignEntry, error)
}
