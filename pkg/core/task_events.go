package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type TaskEvent = task.Event

// TaskSubscription is the core-owned client contract for a task event
// subscription. The concrete *task.Subscription returned by
// OpenTaskEvents/OpenTaskEventsFrom satisfies it; those methods keep their
// concrete return type for existing callers (the pkg/control debug server),
// while mobile consumes events through this interface instead of pkg/task.
type TaskSubscription interface {
	Read(ctx context.Context) ([]TaskEvent, error)
	ReadAvailable() ([]TaskEvent, error)
	Close()
}

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
