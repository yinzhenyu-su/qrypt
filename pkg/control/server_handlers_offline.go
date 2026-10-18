package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

const offlineListDefaultLimit = 20

// offlineActivePlusFailedStates is the default offline list view: active work
// plus failures. Succeeded and canceled tasks are excluded unless --all is set.
func offlineActivePlusFailedStates() []task.State {
	return []task.State{
		task.StateQueued, task.StateScheduled, task.StateRunning, task.StateRetryWait,
		task.StateCanceling, task.StateFailed, task.StatePartialFailed,
	}
}

func (s *CommandServer) offlineServiceOrError(w http.ResponseWriter) (OfflineService, bool) {
	if s == nil || s.offline == nil {
		http.Error(w, "offline control unavailable: no running Core", http.StatusNotImplemented)
		return nil, false
	}
	return s.offline, true
}

// handleOfflineTasks serves POST (create) and GET (list).
func (s *CommandServer) handleOfflineTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleOfflineCreate(w, r)
	case http.MethodGet:
		s.handleOfflineList(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *CommandServer) handleOfflineCreate(w http.ResponseWriter, r *http.Request) {
	service, ok := s.offlineServiceOrError(w)
	if !ok {
		return
	}
	var req OfflineCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid offline create request: "+err.Error(), http.StatusBadRequest)
		return
	}
	created, err := service.CreateOfflineTask(r.Context(), req.OperationRequest())
	if err != nil {
		writeOfflineError(w, err)
		return
	}
	writeJSON(w, created)
}

func (s *CommandServer) handleOfflineList(w http.ResponseWriter, r *http.Request) {
	service, ok := s.offlineServiceOrError(w)
	if !ok {
		return
	}
	query := r.URL.Query()
	limit := offlineListDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	filter := task.Filter{
		Types: []task.Type{task.TypeOfflineDownload, task.TypeOfflineFetch},
		Mount: strings.TrimSpace(query.Get("mount")),
		Limit: limit,
	}
	all := strings.EqualFold(strings.TrimSpace(query.Get("all")), "true")
	if state := strings.TrimSpace(query.Get("state")); state != "" {
		filter.States = []task.State{task.State(state)}
	} else if !all {
		filter.States = offlineActivePlusFailedStates()
	}
	tasks, err := service.ListOfflineTasks(r.Context(), filter)
	if err != nil {
		writeOfflineError(w, err)
		return
	}
	writeJSON(w, OfflineTaskList{SchemaVersion: 1, Tasks: tasks})
}

// OfflineTaskList is the offline list response envelope.
type OfflineTaskList struct {
	SchemaVersion int         `json:"schema_version"`
	Tasks         []task.Task `json:"tasks"`
}

// handleOfflineTask serves /v1/control/offline/tasks/{id}[...].
func (s *CommandServer) handleOfflineTask(w http.ResponseWriter, r *http.Request) {
	service, ok := s.offlineServiceOrError(w)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/control/offline/tasks/")
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
		current, err := service.GetOfflineTask(r.Context(), id)
		if err != nil {
			writeOfflineError(w, err)
			return
		}
		writeJSON(w, current)
	case "cancel":
		s.handleOfflineAction(w, r, id, service.CancelTask)
	case "retry":
		s.handleOfflineAction(w, r, id, service.RetryTask)
	default:
		http.NotFound(w, r)
	}
}

func (s *CommandServer) handleOfflineAction(w http.ResponseWriter, r *http.Request, id string, action func(ctx context.Context, id string) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := action(r.Context(), id); err != nil {
		writeOfflineError(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *CommandServer) handleOfflineInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := s.offlineServiceOrError(w)
	if !ok {
		return
	}
	var req OfflineInspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid offline inspect request: "+err.Error(), http.StatusBadRequest)
		return
	}
	preview, err := service.InspectOffline(r.Context(), req.OperationRequest())
	if err != nil {
		writeOfflineError(w, err)
		return
	}
	writeJSON(w, preview)
}

func (s *CommandServer) handleOfflineWait(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	service, ok := s.offlineServiceOrError(w)
	if !ok {
		return
	}
	var req OfflineWaitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid offline wait request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}
	current, err := service.WaitOfflineTask(r.Context(), req.ID, req.OfflineTimeout())
	if err != nil {
		writeOfflineError(w, err)
		return
	}
	writeJSON(w, current)
}

// writeOfflineError maps a service error to an HTTP status by its class:
// usage problems are 400 (the CLI exits 2), missing tasks are 404, and every
// other runtime failure is 409 (the CLI exits 1).
func writeOfflineError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch OfflineErrorClassOf(err) {
	case OfflineErrorUsage:
		status = http.StatusBadRequest
	case OfflineErrorNotFound:
		status = http.StatusNotFound
	}
	http.Error(w, err.Error(), status)
}
