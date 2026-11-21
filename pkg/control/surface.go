package control

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// MaxWriteBytes is the largest body accepted by /v1/fs/write. It is advertised
// in the capabilities response so a client can size its chunks without
// guessing, and it is the only place the limit is defined.
const MaxWriteBytes = 64 << 20

// TaskJournal is the durable task journal exposed to command clients. It is
// separate from CommandService so a controller that only cancels and retries
// does not have to offer listing.
type TaskJournal interface {
	ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error)
	ListTaskItems(ctx context.Context, id string, filter task.ItemFilter) ([]task.ItemTracking, error)
	TaskPersistenceError() error
}

// MountHealthSource reports per-mount runtime health. It mirrors the
// diagnostic server's checker so health can move onto the command endpoint
// without the product surface reading /v1/debug/*.
type MountHealthSource interface {
	MountHealth(ctx context.Context, mountName string) ([]diagnostics.MountHealth, error)
}

// MountCatalog lists the mounts visible to the Core's namespace, including
// configured mounts that failed to initialize.
type MountCatalog interface {
	Mounts(ctx context.Context) ([]CapabilityMount, error)
}

// SetTaskJournal installs the task journal before Start. Without it
// /v1/control/tasks reports 501 and capabilities omit tasks.list.
func (s *CommandServer) SetTaskJournal(journal TaskJournal) {
	if s == nil {
		return
	}
	s.journal = journal
}

// SetMountHealthSource installs the health checker before Start. Without it
// /v1/control/health still answers, with an explicit mounts_error.
func (s *CommandServer) SetMountHealthSource(source MountHealthSource) {
	if s == nil {
		return
	}
	s.mountHealth = source
}

// SetMountCatalog installs the configured-mount catalog before Start.
func (s *CommandServer) SetMountCatalog(catalog MountCatalog) {
	if s == nil {
		return
	}
	s.catalog = catalog
}

// CapabilityMount is one mount as advertised to clients: enough to build a
// mount picker, not a configuration dump.
type CapabilityMount struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Encrypted bool   `json:"encrypted,omitempty"`
	State     string `json:"state,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CapabilitiesResponse lets a client discover what this Core can do instead of
// probing routes or version-sniffing.
type CapabilitiesResponse struct {
	ProtocolVersion int               `json:"protocol_version"`
	Version         string            `json:"version,omitempty"`
	Features        []string          `json:"features"`
	Mounts          []CapabilityMount `json:"mounts"`
	// WriteBytes is the largest /v1/fs/write body accepted.
	WriteBytes int `json:"write_bytes"`
}

// TaskJournalHealth is the task-journal part of the health response.
type TaskJournalHealth struct {
	Available        bool   `json:"available"`
	PersistenceError string `json:"persistence_error,omitempty"`
}

// RuntimeHealth is the aggregate readiness view: process identity, namespace
// mounts, FUSE sessions, and task persistence.
type RuntimeHealth struct {
	Status         string                    `json:"status"`
	CheckedAt      time.Time                 `json:"checked_at"`
	Runtime        *RuntimeStatus            `json:"runtime,omitempty"`
	RuntimeError   string                    `json:"runtime_error,omitempty"`
	Mounts         []diagnostics.MountHealth `json:"mounts"`
	MountsError    string                    `json:"mounts_error,omitempty"`
	AttachedMounts []MountStatus             `json:"attached_mounts,omitempty"`
	Tasks          TaskJournalHealth         `json:"tasks"`
}

// TaskListResponse is a task list. Paging is not part of protocol version 1.
type TaskListResponse struct {
	Tasks []task.Task `json:"tasks"`
}

// TaskDetailResponse is one task with its item tracking.
type TaskDetailResponse struct {
	Task  task.Task           `json:"task"`
	Items []task.ItemTracking `json:"items"`
}

func (s *CommandServer) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	response := CapabilitiesResponse{
		ProtocolVersion: ProtocolVersion,
		Features:        s.capabilityFeatures(),
		WriteBytes:      MaxWriteBytes,
	}
	if s.runtime != nil {
		if status, err := s.runtime.Status(r.Context()); err == nil {
			response.Version = status.Version
		}
	}
	if s.catalog != nil {
		mounts, err := s.catalog.Mounts(r.Context())
		if err != nil {
			writeUnavailable(w, err.Error())
			return
		}
		if mounts == nil {
			mounts = []CapabilityMount{}
		}
		response.Mounts = mounts
	} else {
		response.Mounts = []CapabilityMount{}
	}
	writeJSON(w, response)
}

// capabilityFeatures derives the feature list from the installed services, so
// the answer cannot drift from what the routes actually do.
func (s *CommandServer) capabilityFeatures() []string {
	// The filesystem routes are registered unconditionally, so the fs features
	// describe the protocol rather than an installed service.
	features := []string{
		"fs.list", "fs.stat", "fs.read", "fs.write", "fs.mkdir",
		"fs.remove", "fs.rename", "fs.truncate", "fs.copy", "fs.space",
		"fs.pending", "fs.wait", "fs.invalidate",
	}
	if s.runtime != nil {
		features = append(features, "runtime.status", "runtime.shutdown")
	}
	if s.mounts != nil {
		features = append(features, "mounts.attach", "mounts.detach")
	}
	if s.service != nil {
		features = append(features, "tasks.control")
	}
	if s.journal != nil {
		features = append(features, "tasks.list")
	}
	if s.mountHealth != nil {
		features = append(features, "health.mounts")
	}
	if s.offline != nil {
		features = append(features, "offline.fetch")
	}
	if s.share != nil {
		features = append(features, "share")
	}
	return features
}

func (s *CommandServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	response := RuntimeHealth{CheckedAt: time.Now()}
	degraded := false
	if s.runtime != nil {
		if status, err := s.runtime.Status(r.Context()); err != nil {
			response.RuntimeError = err.Error()
			degraded = true
		} else {
			response.Runtime = &status
		}
	}
	if s.mountHealth != nil {
		results, err := s.mountHealth.MountHealth(r.Context(), strings.TrimSpace(r.URL.Query().Get("mount")))
		if err != nil {
			response.MountsError = err.Error()
			degraded = true
		}
		if results == nil {
			results = []diagnostics.MountHealth{}
		}
		response.Mounts = results
		for _, mount := range results {
			if !mount.OK {
				degraded = true
			}
		}
	} else {
		response.Mounts = []diagnostics.MountHealth{}
		degraded = true
	}
	if s.mounts != nil {
		if attached, err := s.mounts.ListMounts(r.Context()); err == nil {
			response.AttachedMounts = attached
		}
	}
	if s.journal != nil {
		response.Tasks.Available = true
		if err := s.journal.TaskPersistenceError(); err != nil {
			response.Tasks.PersistenceError = err.Error()
			degraded = true
		}
	}
	response.Status = "ok"
	if degraded {
		response.Status = "degraded"
	}
	writeJSON(w, response)
}

func (s *CommandServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.journal == nil {
		writeNotImplemented(w, "task journal unavailable")
		return
	}
	limit, err := parsePositiveLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	filter := task.Filter{
		ID:     strings.TrimSpace(r.URL.Query().Get("id")),
		Mount:  strings.TrimSpace(r.URL.Query().Get("mount")),
		Path:   strings.TrimSpace(r.URL.Query().Get("path")),
		Types:  taskTypesQuery(r),
		States: taskStatesQuery(r),
		Limit:  limit,
	}
	tasks, err := s.journal.ListTasks(r.Context(), filter)
	if err != nil {
		writeUnavailable(w, err.Error())
		return
	}
	if tasks == nil {
		tasks = []task.Task{}
	}
	writeJSON(w, TaskListResponse{Tasks: tasks})
}

func (s *CommandServer) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.journal == nil {
		writeNotImplemented(w, "task journal unavailable")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/control/tasks/"), "/")
	if id == "" {
		writeBadRequest(w, "task id required")
		return
	}
	tasks, err := s.journal.ListTasks(r.Context(), task.Filter{ID: id, Limit: 1})
	if err != nil {
		writeUnavailable(w, err.Error())
		return
	}
	if len(tasks) == 0 {
		writeNotFound(w, "task not found")
		return
	}
	items, err := s.journal.ListTaskItems(r.Context(), id, task.ItemFilter{})
	if err != nil {
		writeUnavailable(w, err.Error())
		return
	}
	if items == nil {
		items = []task.ItemTracking{}
	}
	writeJSON(w, TaskDetailResponse{Task: tasks[0], Items: items})
}
