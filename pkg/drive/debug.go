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

type debugOperationContextKey struct{}

type DebugOperation struct {
	OpID string `json:"op_id,omitempty"`
	Step string `json:"step,omitempty"`
	Name string `json:"name,omitempty"`
}

func WithDebugOperation(ctx context.Context, op DebugOperation) context.Context {
	return context.WithValue(ctx, debugOperationContextKey{}, op)
}

func DebugOperationFromContext(ctx context.Context) (DebugOperation, bool) {
	op, ok := ctx.Value(debugOperationContextKey{}).(DebugOperation)
	return op, ok
}

type DebugTraceEvent struct {
	At              time.Time      `json:"at"`
	OpID            string         `json:"op_id,omitempty"`
	Step            string         `json:"step,omitempty"`
	Name            string         `json:"name,omitempty"`
	Layer           string         `json:"layer,omitempty"`
	Operation       string         `json:"operation,omitempty"`
	Method          string         `json:"method,omitempty"`
	URL             string         `json:"url,omitempty"`
	Status          int            `json:"status,omitempty"`
	Duration        string         `json:"duration,omitempty"`
	Request         map[string]any `json:"request,omitempty"`
	Response        map[string]any `json:"response,omitempty"`
	Error           string         `json:"error,omitempty"`
	Attempt         int            `json:"attempt,omitempty"`
	Retry           bool           `json:"retry,omitempty"`
	SensitiveMasked bool           `json:"sensitive_masked,omitempty"`
}

type DebugTraceProvider interface {
	DebugTrace(ctx context.Context, since time.Time) ([]DebugTraceEvent, error)
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
