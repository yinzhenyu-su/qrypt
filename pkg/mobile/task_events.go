package mobile

import (
	"context"
	"errors"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

func openTaskEvents(coreID, filterJSON string, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	filter, err := parseTaskFilter(filterJSON)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	sub, err := s.core.OpenTaskEvents(ctx, filter)
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		sub.Close()
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.taskEvents[id] = &taskEventHandle{coreID: coreID, sub: sub}
	registry.mu.Unlock()
	return id, nil
}

func OpenTaskEventsJSON(coreID, filterJSON string, deadlineMS int) string {
	id, err := openTaskEvents(coreID, filterJSON, deadlineMS)
	return resultJSON(id, err)
}

func ReadTaskEventsJSON(handleID string, waitMS int) string {
	handle, err := getTaskEvent(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	if waitMS <= 0 {
		events, err := handle.sub.ReadAvailable()
		if errors.Is(err, context.Canceled) && len(events) > 0 {
			err = nil
		}
		return resultJSON(events, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(waitMS)
	defer cancel()
	events, err := handle.sub.Read(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return resultJSON([]core.TaskEvent{}, nil)
	}
	if errors.Is(err, context.Canceled) && len(events) > 0 {
		err = nil
	}
	return resultJSON(events, wrapError(err))
}

func CloseTaskEventsJSON(handleID string) string {
	handle, err := takeTaskEvent(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	handle.sub.Close()
	return resultJSON(nil, nil)
}
