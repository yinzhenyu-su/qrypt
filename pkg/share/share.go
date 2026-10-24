// Package share is the provider-neutral application contract for share links
// and receive work. Core implements it, the local control transport encodes it,
// and the CLI consumes it; none of those layers owns the DTOs or the service
// interface, so core never depends on a transport package.
package share

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// RedactedPasscode replaces a passcode in command output when the caller did
// not explicitly ask for secrets.
const RedactedPasscode = "****"

// ErrorClass is the transport-level class of a share service error. It is the
// same vocabulary drive uses, so a provider-neutral ShareError already carries
// a class the control server can map onto an HTTP status.
type ErrorClass = drive.ShareErrorClass

// Share error classes.
const (
	ErrorRuntime  = drive.ShareErrorRuntime
	ErrorUsage    = drive.ShareErrorUsage
	ErrorNotFound = drive.ShareErrorNotFound
)

// classified is implemented by errors that know their share class.
type classified interface {
	ShareErrorClass() ErrorClass
}

// ErrorClassOf reports an error's share class. A drive.ShareError carries its
// own class; a request-shape problem is wrapped as a RequestError; everything
// else is a runtime failure.
func ErrorClassOf(err error) ErrorClass {
	if err == nil {
		return ErrorRuntime
	}
	var classed classified
	if errors.As(err, &classed) {
		return classed.ShareErrorClass()
	}
	if provider, ok := drive.AsShareError(err); ok {
		return provider.Class
	}
	if errors.Is(err, task.ErrNotFound) {
		return ErrorNotFound
	}
	return ErrorRuntime
}

// RequestError is a classified share service error. It lets core mark a
// request-shape failure as usage without importing a transport error type.
type RequestError struct {
	Class   ErrorClass
	Code    string
	Message string
	Err     error
}

func (e *RequestError) Error() string {
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

func (e *RequestError) Unwrap() error { return e.Err }

// ShareErrorClass implements classified.
func (e *RequestError) ShareErrorClass() ErrorClass { return e.Class }

// LinkCreateRequest publishes one link for existing entries. Paths must be
// mount-qualified and on one mount; the Core resolves them, never the caller.
// Mount is optional and only confirms the mount the paths already name.
type LinkCreateRequest struct {
	Paths            []string      `json:"paths"`
	Mount            string        `json:"mount,omitempty"`
	Title            string        `json:"title,omitempty"`
	Expiry           time.Duration `json:"expiry,omitempty"`
	Passcode         string        `json:"passcode,omitempty"`
	GeneratePasscode bool          `json:"generate_passcode,omitempty"`
	VisitLimit       int           `json:"visit_limit,omitempty"`
}

// LinkListRequest pages through the acting account's links. An empty Mount
// selects the only configured mount.
type LinkListRequest struct {
	Mount          string `json:"mount,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	IncludeExpired bool   `json:"include_expired,omitempty"`
}

// LinkUpdateRequest changes one link's policy. Nil fields stay unchanged.
type LinkUpdateRequest struct {
	Mount    string         `json:"mount,omitempty"`
	ID       string         `json:"id"`
	Title    *string        `json:"title,omitempty"`
	Expiry   *time.Duration `json:"expiry,omitempty"`
	Passcode *string        `json:"passcode,omitempty"`
	Public   *bool          `json:"public,omitempty"`
}

// LinkList is the link list response envelope.
type LinkList struct {
	SchemaVersion int           `json:"schema_version"`
	Shares        []drive.Share `json:"shares"`
	Next          string        `json:"next,omitempty"`
}

// InspectRequest is the read-only browse input: it opens a link and lists one
// share-relative directory without creating a task.
type InspectRequest struct {
	Link     string `json:"link"`
	Passcode string `json:"passcode,omitempty"`
	Mount    string `json:"mount,omitempty"`
	// Dir is the share-relative directory id; empty means the share root.
	Dir    string `json:"dir,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// ResolveRequest asks which configured mounts can act on a link. Resolution is
// read-only: it parses the link and reads mount configuration, never contacts a
// provider.
type ResolveRequest struct {
	Link string `json:"link"`
}

// ResolveResult reports the link identity and the mounts that recognize it.
// Candidates are sorted; more than one means the caller must choose an account.
type ResolveResult struct {
	Link       drive.ShareLink `json:"link"`
	Candidates []string        `json:"candidates"`
}

// InspectResult is one page of a share directory plus the link metadata.
type InspectResult struct {
	Link    drive.ShareLink    `json:"link"`
	Share   drive.Share        `json:"share"`
	Entries []drive.ShareEntry `json:"entries,omitempty"`
	Next    string             `json:"next,omitempty"`
}

// ReceiveRequest is the provider-neutral creation input of a receive. A
// non-empty DownloadTo selects the fetch task; otherwise the task only copies
// the content into the acting account's drive.
type ReceiveRequest struct {
	Link           string                `json:"link"`
	Passcode       string                `json:"passcode,omitempty"`
	Mount          string                `json:"mount,omitempty"`
	To             string                `json:"to,omitempty"`
	DownloadTo     string                `json:"download_to,omitempty"`
	Selection      task.OfflineSelection `json:"selection,omitempty"`
	Mkdir          bool                  `json:"mkdir,omitempty"`
	KeepCloud      bool                  `json:"keep_cloud,omitempty"`
	Workers        int                   `json:"workers,omitempty"`
	ChunkSize      int64                 `json:"chunk_size,omitempty"`
	New            bool                  `json:"new,omitempty"`
	IdempotencyKey string                `json:"idempotency_key,omitempty"`
}

// OperationRequest converts the create input into the shared operation request.
// A download destination selects the download operation, which the Core maps
// onto share_fetch; otherwise the request rides the share operation.
func (r ReceiveRequest) OperationRequest() task.OperationRequest {
	kind := task.OperationShare
	if strings.TrimSpace(r.DownloadTo) != "" {
		kind = task.OperationDownload
	}
	return task.OperationRequest{
		Operation: kind,
		Share: &task.ShareOperationRequest{
			Link:       r.Link,
			Passcode:   r.Passcode,
			Mount:      r.Mount,
			To:         r.To,
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

// ReceiveListRequest is the receive task list query.
type ReceiveListRequest struct {
	All   bool   `json:"all,omitempty"`
	State string `json:"state,omitempty"`
	Mount string `json:"mount,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// TaskList is the receive task list response envelope.
type TaskList struct {
	SchemaVersion int         `json:"schema_version"`
	Tasks         []task.Task `json:"tasks"`
}

// WaitRequest asks to observe one receive task to a terminal state. A
// non-positive timeout waits indefinitely.
type WaitRequest struct {
	ID            string `json:"id"`
	TimeoutMillis int64  `json:"timeout_ms,omitempty"`
}

// Timeout returns the configured wait timeout, or zero for indefinite.
func (r WaitRequest) Timeout() time.Duration {
	if r.TimeoutMillis <= 0 {
		return 0
	}
	return time.Duration(r.TimeoutMillis) * time.Millisecond
}

// Service is the provider-neutral share surface the transport exposes and Core
// implements. No method requires a provider name, driver name, socket path, or
// provider task identifier.
type Service interface {
	CreateShareLink(ctx context.Context, req LinkCreateRequest) (drive.Share, error)
	ListShareLinks(ctx context.Context, req LinkListRequest) (drive.SharePage, error)
	GetShareLink(ctx context.Context, mount, id string) (drive.Share, error)
	UpdateShareLink(ctx context.Context, req LinkUpdateRequest) (drive.Share, error)
	RemoveShareLink(ctx context.Context, mount, id string) error
	ResolveShareLink(ctx context.Context, req ResolveRequest) (ResolveResult, error)
	InspectShareLink(ctx context.Context, req InspectRequest) (InspectResult, error)

	CreateShareTask(ctx context.Context, req task.OperationRequest) (task.Task, error)
	ListShareTasks(ctx context.Context, filter task.Filter) ([]task.Task, error)
	GetShareTask(ctx context.Context, id string) (task.Task, error)
	WaitShareTask(ctx context.Context, id string, timeout time.Duration) (task.Task, error)
	CancelTask(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string) error
}

// RedactLink returns a copy safe for output. The passcode is masked unless the
// caller explicitly asks for secrets.
func RedactLink(link drive.Share, revealSecrets bool) drive.Share {
	if !revealSecrets && link.Passcode != "" {
		link.Passcode = RedactedPasscode
	}
	return link
}

// RedactLinks redacts a page of links for output.
func RedactLinks(links []drive.Share, revealSecrets bool) []drive.Share {
	out := make([]drive.Share, len(links))
	for i, link := range links {
		out[i] = RedactLink(link, revealSecrets)
	}
	return out
}

// RedactTask returns a copy of a receive task whose detail hides the passcode
// unless secrets were requested. The stored task is never mutated.
func RedactTask(item task.Task, revealSecrets bool) task.Task {
	if revealSecrets || item.Detail == nil {
		return item
	}
	detail, ok := detailMap(item.Detail[task.ShareDetailKey])
	if !ok {
		return item
	}
	if passcode, _ := detail["passcode"].(string); passcode != "" {
		detail["passcode"] = RedactedPasscode
	}
	// A provider session token never belongs in a default view.
	for _, key := range []string{"token", "stoken", "session_token"} {
		if text, _ := detail[key].(string); text != "" {
			detail[key] = RedactedPasscode
		}
	}
	redacted := item
	redacted.Detail = make(map[string]any, len(item.Detail))
	for key, entry := range item.Detail {
		redacted.Detail[key] = entry
	}
	redacted.Detail[task.ShareDetailKey] = detail
	return redacted
}

// TaskDetail reads the durable receive metadata from a task snapshot. A typed
// detail written by Core is converted to its JSON shape, so callers do not need
// to know which representation is in memory.
func TaskDetail(item task.Task) map[string]any {
	if item.Detail == nil {
		return nil
	}
	detail, _ := detailMap(item.Detail[task.ShareDetailKey])
	return detail
}

// detailMap normalizes a detail value to a detached map. JSON round-tripping
// covers the typed detail Core stores in memory; a map is shallow-copied so the
// caller can redact without mutating the task.
func detailMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if typed, ok := value.(map[string]any); ok {
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = entry
		}
		return out, true
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

// DetailString reads a string field from the receive detail map.
func DetailString(detail map[string]any, key string) string {
	if detail == nil {
		return ""
	}
	value, _ := detail[key].(string)
	return strings.TrimSpace(value)
}
