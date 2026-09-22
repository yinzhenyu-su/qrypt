package mobile

import (
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/core"
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
	tasks, err := withCore(s, func(c *core.Core) ([]core.Task, error) { return c.ListTasks(s.ctx, filter) })
	if tasks == nil {
		tasks = []core.Task{}
	}
	return resultJSON(tasks, err)
}

func GetTaskJSON(coreID, taskID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	item, err := withCore(s, func(c *core.Core) (core.Task, error) { return c.GetTask(s.ctx, taskID) })
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
	items, err := withCore(s, func(c *core.Core) ([]core.TaskItemResult, error) {
		return c.ListTaskItems(s.ctx, taskID, filter)
	})
	return resultJSON(items, err)
}

func GetTaskItemJSON(coreID, taskID, itemID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	item, err := withCore(s, func(c *core.Core) (core.TaskItemResult, error) {
		return c.GetTaskItem(s.ctx, taskID, itemID)
	})
	return resultJSON(item, err)
}

func CancelTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.CancelTask(ctx, taskID) }))
}

func CancelTaskItemJSON(coreID, taskID, itemID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.CancelTaskItem(ctx, taskID, itemID) }))
}

func RetryTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.RetryTask(ctx, taskID) }))
}

func DismissTaskJSON(coreID, taskID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.DismissTask(ctx, taskID) }))
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
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	removed, err := withCore(s, func(c *core.Core) (int, error) { return c.DismissFinishedTasks(ctx, filter) })
	return resultJSON(map[string]int{"removed": removed}, err)
}

func CreateTaskJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req core.TaskRequest
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	item, err := withCore(s, func(c *core.Core) (core.Task, error) { return c.CreateTask(ctx, req) })
	return resultJSON(item, err)
}

func CreateOperationJSON(coreID, requestRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var req core.TaskOperationRequest
	if err := json.Unmarshal([]byte(requestRaw), &req); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	item, err := withCore(s, func(c *core.Core) (core.Task, error) { return c.CreateOperation(ctx, req) })
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
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	item, err := withCore(s, func(c *core.Core) (core.Task, error) { return c.CreateLocalUploadTask(ctx, req) })
	return resultJSON(item, err)
}

func parseTaskFilter(raw string) (core.TaskFilter, error) {
	if raw == "" {
		return core.TaskFilter{}, nil
	}
	var filter core.TaskFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return core.TaskFilter{}, err
	}
	return filter, nil
}

func applyDefaultMobileTaskFilter(filter *core.TaskFilter) {
	if filter == nil || filter.ID != "" || len(filter.Types) > 0 || filter.Scope != "" {
		return
	}
	// Mobile task lists default to user-origin tasks. Internal-scope
	// bookkeeping (VFS upload_remote/delete_remote records) stays out of
	// the UI list.
	filter.Scope = core.TaskScopeUser
}

func parseTaskItemFilter(raw string) (core.TaskItemFilter, error) {
	if raw == "" {
		return core.TaskItemFilter{}, nil
	}
	var filter core.TaskItemFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return core.TaskItemFilter{}, err
	}
	return filter, nil
}
