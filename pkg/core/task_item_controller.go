package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type TaskItemHandle interface {
	Close() error
}

type taskItemController interface {
	listItems(task.ItemFilter) []task.ItemTracking
	cancelItem(context.Context, string) error
	openItem(context.Context, string, task.Action) (TaskItemHandle, error)
	commitItem(context.Context, string) error
}

type uploadStreamItemController struct {
	core  *Core
	batch *uploadStreamBatch
}

func (c uploadStreamItemController) cancelItem(ctx context.Context, itemID string) error {
	return c.core.cancelUploadStreamItem(ctx, c.batch, itemID)
}

func (c uploadStreamItemController) listItems(filter task.ItemFilter) []task.ItemTracking {
	c.batch.mu.Lock()
	defer c.batch.mu.Unlock()
	return filterTaskItems(c.batch.trackingItemsLocked(), filter)
}

func (c uploadStreamItemController) openItem(ctx context.Context, itemID string, action task.Action) (TaskItemHandle, error) {
	if action != task.ActionOpenInput {
		return nil, fmt.Errorf("core: item action %q is unsupported for upload input", action)
	}
	return c.core.OpenUploadStreamItem(ctx, c.batch.taskID, itemID)
}

func (c uploadStreamItemController) commitItem(ctx context.Context, itemID string) error {
	return c.core.CommitStagedUploadItem(ctx, c.batch.taskID, itemID)
}

type downloadStreamItemController struct {
	core  *Core
	batch *downloadStreamBatch
}

func (c downloadStreamItemController) cancelItem(ctx context.Context, itemID string) error {
	return c.core.cancelDownloadStreamItem(ctx, c.batch, itemID)
}

func (c downloadStreamItemController) listItems(filter task.ItemFilter) []task.ItemTracking {
	c.batch.mu.Lock()
	defer c.batch.mu.Unlock()
	return filterTaskItems(c.batch.trackingItemsLocked(), filter)
}

func (c downloadStreamItemController) openItem(ctx context.Context, itemID string, action task.Action) (TaskItemHandle, error) {
	if action != task.ActionOpenOutput {
		return nil, fmt.Errorf("core: item action %q is unsupported for download output", action)
	}
	return c.core.OpenDownloadStreamItem(ctx, c.batch.taskID, itemID)
}

func (c downloadStreamItemController) commitItem(context.Context, string) error {
	return fmt.Errorf("core: commit input is unsupported for download output")
}

func (c *Core) itemController(taskID string) (taskItemController, error) {
	if batch := c.getUploadStream(taskID); batch != nil {
		return uploadStreamItemController{core: c, batch: batch}, nil
	}
	if batch := c.getDownloadStream(taskID); batch != nil {
		return downloadStreamItemController{core: c, batch: batch}, nil
	}
	return nil, fmt.Errorf("core: task item controller not found")
}

func filterTaskItems(items []task.ItemTracking, filter task.ItemFilter) []task.ItemTracking {
	out := make([]task.ItemTracking, 0, len(items))
	for _, item := range items {
		if !filter.Match(item) {
			continue
		}
		out = append(out, item)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out
}

var _ taskItemController = uploadStreamItemController{}
var _ taskItemController = downloadStreamItemController{}
