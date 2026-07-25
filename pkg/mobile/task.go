package mobile

import (
	"context"
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

func ListTasksJSON(coreID, filterRaw string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	filter, err := parseTaskFilter(filterRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	tasks, err := s.core.ListTasks(context.Background(), filter)
	return resultJSON(tasks, err)
}

func GetTaskJSON(coreID, taskID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	item, err := s.core.GetTask(context.Background(), taskID)
	return resultJSON(item, err)
}

func CancelTaskJSON(coreID, taskID string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.CancelTask(ctx, taskID))
}

func RetryTaskJSON(coreID, taskID string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.RetryTask(ctx, taskID))
}

func CreateMoveTaskJSON(coreID, requestRaw string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req core.MoveTaskRequest
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.CreateMoveTask(ctx, req)
	return resultJSON(item, err)
}

func parseTaskFilter(raw string) (task.Filter, error) {
	if raw == "" {
		return task.Filter{}, nil
	}
	var filter task.Filter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return task.Filter{}, err
	}
	return filter, nil
}
