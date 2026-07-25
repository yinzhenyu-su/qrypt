package core

import (
	"context"
	"fmt"
	"sort"

	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type Task = task.Task
type TaskFilter = task.Filter
type TaskType = task.Type
type TaskState = task.State

type taskManager interface {
	Tasks(task.Filter) []task.Task
	CancelTask(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string) error
}

func (c *Core) ListTasks(ctx context.Context, filter task.Filter) ([]task.Task, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager, ok := c.fs.(taskManager)
	if !ok {
		return nil, fmt.Errorf("core: tasks are not supported")
	}
	tasks := manager.Tasks(filter)
	tasks = append(tasks, c.moveTaskSnapshot(filter)...)
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	if filter.Limit > 0 && len(tasks) > filter.Limit {
		tasks = tasks[:filter.Limit]
	}
	return tasks, nil
}

func (c *Core) GetTask(ctx context.Context, id string) (task.Task, error) {
	if id == "" {
		return task.Task{}, fmt.Errorf("core: task id required")
	}
	tasks, err := c.ListTasks(ctx, task.Filter{})
	if err != nil {
		return task.Task{}, err
	}
	for _, item := range tasks {
		if item.ID == id {
			return item, nil
		}
	}
	return task.Task{}, fmt.Errorf("core: task %q not found", id)
}

func (c *Core) CancelTask(ctx context.Context, id string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if id == "" {
		return fmt.Errorf("core: task id required")
	}
	manager, ok := c.fs.(taskManager)
	if !ok {
		return fmt.Errorf("core: tasks are not supported")
	}
	if err := manager.CancelTask(ctx, id); err != nil {
		if !vfs.IsNotFound(err) {
			return err
		}
	} else {
		return nil
	}
	if c.hasMoveTask(id) {
		return fmt.Errorf("core: move task %q is not cancelable", id)
	}
	return fmt.Errorf("core: task %q not found", id)
}

func (c *Core) RetryTask(ctx context.Context, id string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if id == "" {
		return fmt.Errorf("core: task id required")
	}
	manager, ok := c.fs.(taskManager)
	if !ok {
		return fmt.Errorf("core: tasks are not supported")
	}
	if err := manager.RetryTask(ctx, id); err != nil {
		if !vfs.IsNotFound(err) {
			return err
		}
	} else {
		return nil
	}
	if c.hasMoveTask(id) {
		return fmt.Errorf("core: move task %q is not retryable", id)
	}
	return fmt.Errorf("core: task %q not found", id)
}
