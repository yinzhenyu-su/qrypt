package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type TaskEvent = task.Event

func (c *Core) OpenTaskEvents(ctx context.Context, filter task.Filter) (*task.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager, err := c.taskManager()
	if err != nil {
		return nil, err
	}
	return manager.Subscribe(filter), nil
}
