package mobile

import (
	"context"
	"errors"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func openTaskEvents(coreID, filterJSON string, afterSeq uint64, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	filter, err := parseTaskFilter(filterJSON)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	sub, err := withCore(s, func(c *core.Core) (*task.Subscription, error) {
		return c.OpenTaskEventsFrom(ctx, filter, afterSeq)
	})
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
	id, err := openTaskEvents(coreID, filterJSON, 0, deadlineMS)
	return resultJSON(id, err)
}

func OpenTaskEventsFromJSON(coreID, filterJSON string, afterSeq int64, deadlineMS int) string {
	if afterSeq < 0 {
		return resultJSON(nil, wrapError(fmt.Errorf("mobile: after sequence must be non-negative")))
	}
	id, err := openTaskEvents(coreID, filterJSON, uint64(afterSeq), deadlineMS)
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
	s, err := getSession(handle.coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(waitMS)
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
