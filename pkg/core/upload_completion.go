package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type uploadCompletion interface {
	Wait(ctx context.Context, remotePath string) (task.Task, bool, error)
}

type taskUploadCompletion struct {
	core *Core
}

func (c *Core) uploadCompletion() uploadCompletion {
	return taskUploadCompletion{core: c}
}

func (w taskUploadCompletion) Wait(ctx context.Context, remotePath string) (task.Task, bool, error) {
	return w.core.waitUploadTaskForPath(ctx, remotePath)
}
