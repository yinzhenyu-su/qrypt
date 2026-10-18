package control

import (
	"context"
	"errors"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// OfflineErrorClass is the transport-level class of an offline service error.
// The control server maps it to an HTTP status so the CLI can pick the process
// exit code without parsing provider-specific text.
type OfflineErrorClass uint8

const (
	OfflineErrorRuntime OfflineErrorClass = iota
	OfflineErrorUsage
	OfflineErrorNotFound
)

// offlineClassified is implemented by errors that know their class. Core's
// Usage and NotFound are honoured; everything else is a runtime failure.
type offlineClassified interface {
	OfflineErrorClass() OfflineErrorClass
}

// OfflineErrorClassOf reports an error's offline class.
func OfflineErrorClassOf(err error) OfflineErrorClass {
	var classified offlineClassified
	if errors.As(err, &classified) {
		return classified.OfflineErrorClass()
	}
	return OfflineErrorRuntime
}

// OfflineError is a classified service error. It exists so pkg/core and
// pkg/control share a single error contract without importing each other's
// error types.
type OfflineError struct {
	Class   OfflineErrorClass
	Code    string
	Message string
	Err     error
}

func (e *OfflineError) Error() string {
	if e == nil {
		return ""
	}
	message := e.Message
	if message == "" && e.Err != nil {
		message = e.Err.Error()
	}
	if e.Code != "" {
		return e.Code + ": " + message
	}
	return message
}

func (e *OfflineError) Unwrap() error { return e.Err }

// OfflineErrorClass implements offlineClassified.
func (e *OfflineError) OfflineErrorClass() OfflineErrorClass { return e.Class }

// OfflineCreateRequest is the provider-neutral creation input of the offline
// control surface. It carries no provider name, driver name, socket path, or
// provider task identifier.
type OfflineCreateRequest struct {
	Source     string                `json:"source"`
	SourceKind string                `json:"source_kind,omitempty"`
	Title      string                `json:"title,omitempty"`
	Mount      string                `json:"mount,omitempty"`
	ParseTo    string                `json:"parse_to,omitempty"`
	DownloadTo string                `json:"download_to,omitempty"`
	Selection  task.OfflineSelection `json:"selection,omitempty"`
	Mkdir      bool                  `json:"mkdir,omitempty"`
	New        bool                  `json:"new,omitempty"`
	KeepCloud  bool                  `json:"keep_cloud,omitempty"`
	// Workers and ChunkSize tune the fetch transfer; zero selects the Core
	// defaults. They are rejected for a cloud-only task.
	Workers        int    `json:"workers,omitempty"`
	ChunkSize      int64  `json:"chunk_size,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// OperationRequest converts the create input into the shared operation
// request: offline work always rides the download operation, and a final
// destination selects the fetch task type.
func (r OfflineCreateRequest) OperationRequest() task.OperationRequest {
	return task.OperationRequest{
		Operation: task.OperationDownload,
		Offline: &task.OfflineOperationRequest{
			SourceKind: r.SourceKind,
			Source:     r.Source,
			Title:      r.Title,
			Mount:      r.Mount,
			ParseTo:    r.ParseTo,
			DownloadTo: r.DownloadTo,
			Selection:  r.Selection,
			Mkdir:      r.Mkdir,
			New:        r.New,
			KeepCloud:  r.KeepCloud,
			Workers:    r.Workers,
			ChunkSize:  r.ChunkSize,
		},
		IdempotencyKey: r.IdempotencyKey,
	}
}

// OfflineInspectRequest is the read-only inspection input.
type OfflineInspectRequest struct {
	Source     string                `json:"source"`
	SourceKind string                `json:"source_kind,omitempty"`
	Title      string                `json:"title,omitempty"`
	Mount      string                `json:"mount,omitempty"`
	ParseTo    string                `json:"parse_to,omitempty"`
	Selection  task.OfflineSelection `json:"selection,omitempty"`
}

// OperationRequest converts the inspect input into an offline request with no
// final destination.
func (r OfflineInspectRequest) OperationRequest() task.OfflineOperationRequest {
	return task.OfflineOperationRequest{
		SourceKind: r.SourceKind,
		Source:     r.Source,
		Title:      r.Title,
		Mount:      r.Mount,
		ParseTo:    r.ParseTo,
		Selection:  r.Selection,
	}
}

// OfflinePreviewFile is one parsed file with the request's selection applied.
type OfflinePreviewFile struct {
	Index    int    `json:"index"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	RemoteID string `json:"remote_id,omitempty"`
	Selected bool   `json:"selected"`
}

// OfflinePreviewResult is the read-only inspection result.
type OfflinePreviewResult struct {
	Source      string               `json:"source"`
	SourceKind  string               `json:"source_kind,omitempty"`
	Title       string               `json:"title,omitempty"`
	TotalSize   int64                `json:"total_size,omitempty"`
	Files       []OfflinePreviewFile `json:"files,omitempty"`
	InstantSave bool                 `json:"instant_save,omitempty"`
	Mount       string               `json:"mount,omitempty"`
	ParseDir    string               `json:"parse_dir,omitempty"`
	ParseUsable bool                 `json:"parse_usable"`
	ParseError  string               `json:"parse_error,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
}

// OfflineWaitRequest asks to observe one offline task to a terminal state. A
// non-positive timeout waits indefinitely.
type OfflineWaitRequest struct {
	ID            string `json:"id"`
	TimeoutMillis int64  `json:"timeout_ms,omitempty"`
}

// OfflineTimeout returns the configured wait timeout, or zero for indefinite.
func (r OfflineWaitRequest) OfflineTimeout() time.Duration {
	if r.TimeoutMillis <= 0 {
		return 0
	}
	return time.Duration(r.TimeoutMillis) * time.Millisecond
}

// OfflineListRequest is the offline task list query.
type OfflineListRequest struct {
	All   bool   `json:"all,omitempty"`
	State string `json:"state,omitempty"`
	Mount string `json:"mount,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// OfflineService is the provider-neutral offline surface the control server
// exposes. Core implements it; the transport never learns a provider name.
type OfflineService interface {
	CreateOfflineTask(ctx context.Context, req task.OperationRequest) (task.Task, error)
	ListOfflineTasks(ctx context.Context, filter task.Filter) ([]task.Task, error)
	GetOfflineTask(ctx context.Context, id string) (task.Task, error)
	InspectOffline(ctx context.Context, req task.OfflineOperationRequest) (OfflinePreviewResult, error)
	WaitOfflineTask(ctx context.Context, id string, timeout time.Duration) (task.Task, error)
	CancelTask(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string) error
}

// SetOfflineService attaches the provider-neutral offline surface. Without it
// the offline routes report an unavailable Core.
func (s *CommandServer) SetOfflineService(service OfflineService) {
	if s != nil {
		s.offline = service
	}
}
