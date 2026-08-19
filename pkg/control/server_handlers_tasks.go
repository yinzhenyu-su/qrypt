package control

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.taskDebug == nil {
		http.Error(w, "tasks unavailable", http.StatusNotImplemented)
		return
	}
	limit, err := parsePositiveLimit(r.URL.Query().Get("limit"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	tasks, err := s.taskDebug.ListTasks(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	debugTasks := make([]DebugTask, 0, len(tasks))
	for _, item := range tasks {
		items, err := s.taskDebug.ListTaskItems(r.Context(), item.ID, task.ItemFilter{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		debugTasks = append(debugTasks, DebugTask{Task: item, Items: items})
	}
	writeJSON(w, TasksResponse{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Tasks:         debugTasks,
	})
}

func parsePositiveLimit(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 {
		return 0, errors.New("limit must be a positive integer")
	}
	return limit, nil
}

func taskTypesQuery(r *http.Request) []task.Type {
	values := append([]string{}, r.URL.Query()["type"]...)
	var out []task.Type
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, task.Type(part))
			}
		}
	}
	return out
}

func taskStatesQuery(r *http.Request) []task.State {
	values := append([]string{}, r.URL.Query()["state"]...)
	var out []task.State
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, task.State(part))
			}
		}
	}
	return out
}
