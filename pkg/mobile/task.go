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
	applyDefaultMobileTaskFilter(&filter)
	tasks, err := s.core.ListTasks(context.Background(), filter)
	if tasks == nil {
		tasks = []task.Task{}
	}
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

func ListTaskItemsJSON(coreID, taskID, filterRaw string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	filter, err := parseTaskItemFilter(filterRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	items, err := s.core.ListTaskItems(context.Background(), taskID, filter)
	return resultJSON(items, err)
}

func GetTaskItemJSON(coreID, taskID, itemID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	item, err := s.core.GetTaskItem(context.Background(), taskID, itemID)
	return resultJSON(item, err)
}

func CancelTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.CancelTask(ctx, taskID))
}

func CancelTaskItemJSON(coreID, taskID, itemID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.CancelTaskItem(ctx, taskID, itemID))
}

func RetryTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.RetryTask(ctx, taskID))
}

func DismissTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.DismissTask(ctx, taskID))
}

func DismissFinishedTasksJSON(coreID, filterRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	filter, err := parseTaskFilter(filterRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	removed, err := s.core.DismissFinishedTasks(ctx, filter)
	return resultJSON(map[string]int{"removed": removed}, err)
}

func CreateTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req task.Request
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.CreateTask(ctx, req)
	return resultJSON(item, err)
}

func CreateUploadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req task.Request
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	req.Type = task.TypeUploadStreamBatch
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.CreateTask(ctx, req)
	return resultJSON(item, err)
}

func CreateLocalUploadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req core.LocalUploadTaskRequest
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.CreateLocalUploadTask(ctx, req)
	return resultJSON(item, err)
}

func CreateDownloadTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req task.Request
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	req.Type = task.TypeDownloadStreamBatch
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	item, err := s.core.CreateTask(ctx, req)
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

func applyDefaultMobileTaskFilter(filter *task.Filter) {
	if filter == nil || filter.ID != "" || len(filter.Types) > 0 || filter.Scope != "" {
		return
	}
	// Mobile task lists default to user-visible tasks. Sync-scope tasks
	// (VFS upload_remote/delete_remote bookkeeping) stay out of the UI list.
	filter.Scope = task.ScopeUser
}

func parseTaskItemFilter(raw string) (task.ItemFilter, error) {
	if raw == "" {
		return task.ItemFilter{}, nil
	}
	var filter task.ItemFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return task.ItemFilter{}, err
	}
	return filter, nil
}
