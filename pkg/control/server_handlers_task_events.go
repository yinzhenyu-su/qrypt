package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.taskEvents == nil {
		http.Error(w, "task events unavailable", http.StatusNotImplemented)
		return
	}
	afterSeq, err := parseTaskEventSequence(r.URL.Query().Get("after_seq"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wait, err := parseTaskEventWait(r.URL.Query().Get("wait_ms"))
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
	}
	subscription, err := s.taskEvents.OpenTaskEventsFrom(r.Context(), filter, afterSeq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer subscription.Close()

	var events []task.Event
	if wait > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		defer cancel()
		events, err = subscription.Read(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
	} else {
		events, err = subscription.ReadAvailable()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if events == nil {
		events = []task.Event{}
	}
	writeJSON(w, TaskEventsResponse{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Now(),
		Events:        events,
	})
}

func parseTaskEventSequence(raw string) (uint64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("after_seq must be a non-negative integer")
	}
	return seq, nil
}

func parseTaskEventWait(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	waitMS, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || waitMS < 0 {
		return 0, errors.New("wait_ms must be a non-negative integer")
	}
	return time.Duration(waitMS) * time.Millisecond, nil
}
