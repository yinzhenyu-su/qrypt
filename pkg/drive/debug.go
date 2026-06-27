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

type HealthStatus struct {
	Driver    string         `json:"driver"`
	OK        bool           `json:"ok"`
	CheckedAt time.Time      `json:"checked_at"`
	Latency   string         `json:"latency,omitempty"`
	Error     string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type HealthChecker interface {
	HealthCheck(ctx context.Context) HealthStatus
}

type RemoteNameInfo struct {
	PlainName  string `json:"plain_name"`
	RemoteName string `json:"remote_name"`
}

type RemoteNameResolver interface {
	ResolveRemoteName(ctx context.Context, plainName string) (RemoteNameInfo, error)
}
