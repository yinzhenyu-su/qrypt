package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type TaskEvent = task.Event

func (c *Core) OpenTaskEvents(ctx context.Context, filter task.Filter) (*task.Subscription, error) {
	return c.OpenTaskEventsFrom(ctx, filter, 0)
}

func (c *Core) OpenTaskEventsFrom(ctx context.Context, filter task.Filter, afterSeq uint64) (*task.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager, err := c.taskManager()
	if err != nil {
		return nil, err
	}
	return manager.SubscribeFrom(filter, afterSeq), nil
}
