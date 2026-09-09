package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type uploadStreamRun func(context.Context) error

func (c *Core) startUploadStreamTask(update task.UpdateFunc, batch *uploadStreamBatch) {
	batch.mu.Lock()
	batch.update = update
	batch.readyOnce.Do(func() { close(batch.ready) })
	batch.mu.Unlock()
	batch.updateTaskSnapshot()
}

func (c *Core) runUploadStreamLifecycle(ctx context.Context, update task.UpdateFunc, batch *uploadStreamBatch, run uploadStreamRun) error {
	c.startUploadStreamTask(update, batch)
	defer c.removeUploadStream(batch.taskID)
	if err := run(ctx); err != nil {
		return err
	}
	return c.finishUploadStreamTask(update, batch)
}

func (c *Core) finishUploadStreamTask(update task.UpdateFunc, batch *uploadStreamBatch) error {
	batch.mu.Lock()
	batch.closeDoneIfTerminalLocked()
	batch.mu.Unlock()
	return batch.finishTask(update)
}

func (c *Core) cancelUploadStreamTask(ctx context.Context, batch *uploadStreamBatch) error {
	if err := ctx.Err(); err != nil {
		batch.markCanceled(err)
		batch.updateTaskSnapshot()
		return err
	}
	return nil
}
