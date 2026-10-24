package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	shareapi "github.com/yinzhenyu/qrypt/pkg/share"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

const shareListDefaultLimit = 20

// shareActivePlusFailedStates is the default receive list view: active work
// plus failures. Succeeded and canceled tasks are excluded unless --all is set.
func shareActivePlusFailedStates() []task.State {
	return []task.State{
		task.StateQueued, task.StateScheduled, task.StateRunning, task.StateRetryWait,
		task.StateCanceling, task.StateFailed, task.StatePartialFailed,
	}
}

func (s *CommandServer) shareServiceOrError(w http.ResponseWriter) (shareapi.Service, bool) {
	if s == nil || s.share == nil {
		http.Error(w, "share control unavailable: no running Core", http.StatusNotImplemented)
		return nil, false
	}
	return s.share, true
}

// handleShareLinks serves GET (list) and POST (create).
func (s *CommandServer) handleShareLinks(w http.ResponseWriter, r *http.Request) {
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req shareapi.LinkCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid share link create request: "+err.Error(), http.StatusBadRequest)
			return
		}
		created, err := service.CreateShareLink(r.Context(), req)
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, created)
	case http.MethodGet:
		query := r.URL.Query()
		limit := shareListDefaultLimit
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		page, err := service.ListShareLinks(r.Context(), shareapi.LinkListRequest{
			Mount:          strings.TrimSpace(query.Get("mount")),
			Cursor:         strings.TrimSpace(query.Get("cursor")),
			Limit:          limit,
			IncludeExpired: strings.EqualFold(strings.TrimSpace(query.Get("all")), "true"),
		})
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, shareapi.LinkList{SchemaVersion: 1, Shares: page.Shares, Next: page.Next})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleShareLink serves /v1/control/share/links/{id}: GET, PATCH, DELETE.
func (s *CommandServer) handleShareLink(w http.ResponseWriter, r *http.Request) {
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/control/share/links/"), "/")
	if id == "" {
		http.Error(w, "link id required", http.StatusBadRequest)
		return
	}
	mount := strings.TrimSpace(r.URL.Query().Get("mount"))
	switch r.Method {
	case http.MethodGet:
		share, err := service.GetShareLink(r.Context(), mount, id)
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, share)
	case http.MethodPatch:
		var req shareapi.LinkUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid share link update request: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.ID = id
		if req.Mount == "" {
			req.Mount = mount
		}
		updated, err := service.UpdateShareLink(r.Context(), req)
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, updated)
	case http.MethodDelete:
		if err := service.RemoveShareLink(r.Context(), mount, id); err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, map[string]string{"id": id})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *CommandServer) handleShareInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	var req shareapi.InspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid share inspect request: "+err.Error(), http.StatusBadRequest)
		return
	}
	preview, err := service.InspectShareLink(r.Context(), req)
	if err != nil {
		writeShareError(w, err)
		return
	}
	writeJSON(w, preview)
}

// handleShareTasks serves POST (create) and GET (list).
func (s *CommandServer) handleShareTasks(w http.ResponseWriter, r *http.Request) {
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req shareapi.ReceiveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid receive create request: "+err.Error(), http.StatusBadRequest)
			return
		}
		created, err := service.CreateShareTask(r.Context(), req.OperationRequest())
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, created)
	case http.MethodGet:
		query := r.URL.Query()
		limit := shareListDefaultLimit
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		filter := task.Filter{
			Mount: strings.TrimSpace(query.Get("mount")),
			Limit: limit,
		}
		all := strings.EqualFold(strings.TrimSpace(query.Get("all")), "true")
		if state := strings.TrimSpace(query.Get("state")); state != "" {
			filter.States = []task.State{task.State(state)}
		} else if !all {
			filter.States = shareActivePlusFailedStates()
		}
		tasks, err := service.ListShareTasks(r.Context(), filter)
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, shareapi.TaskList{SchemaVersion: 1, Tasks: tasks})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleShareTask serves /v1/control/share/tasks/{id}[...].
func (s *CommandServer) handleShareTask(w http.ResponseWriter, r *http.Request) {
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/control/share/tasks/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	switch action {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		current, err := service.GetShareTask(r.Context(), id)
		if err != nil {
			writeShareError(w, err)
			return
		}
		writeJSON(w, current)
	case "cancel":
		s.handleShareAction(w, r, id, service.CancelTask)
	case "retry":
		s.handleShareAction(w, r, id, service.RetryTask)
	default:
		http.NotFound(w, r)
	}
}

func (s *CommandServer) handleShareAction(w http.ResponseWriter, r *http.Request, id string, action func(ctx context.Context, id string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := action(r.Context(), id); err != nil {
		writeShareError(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *CommandServer) handleShareWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := s.shareServiceOrError(w)
	if !ok {
		return
	}
	var req shareapi.WaitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid share wait request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}
	current, err := service.WaitShareTask(r.Context(), req.ID, req.Timeout())
	if err != nil {
		writeShareError(w, err)
		return
	}
	writeJSON(w, current)
}

// writeShareError maps a service error to an HTTP status by its class: usage
// problems are 400 (the CLI exits 2), missing links and tasks are 404, and
// every other runtime failure is 409 (the CLI exits 1).
func writeShareError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch shareapi.ErrorClassOf(err) {
	case shareapi.ErrorUsage:
		status = http.StatusBadRequest
	case shareapi.ErrorNotFound:
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}

// SetShareService attaches the provider-neutral share surface. Without it the
// share routes report an unavailable Core.
func (s *CommandServer) SetShareService(service shareapi.Service) {
	if s != nil {
		s.share = service
	}
}
