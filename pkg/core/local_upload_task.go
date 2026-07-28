package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

type LocalUploadTaskRequest struct {
	Items      []LocalUploadTaskItem     `json:"items,omitempty"`
	Options    task.Options              `json:"options,omitempty"`
	Stability  LocalFileStabilityOptions `json:"stability,omitempty"`
	WaitStable bool                      `json:"wait_stable,omitempty"`
}

type LocalUploadTaskItem struct {
	LocalPath string `json:"local_path,omitempty"`
	DestPath  string `json:"dest_path,omitempty"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name,omitempty"`
	ItemID    string `json:"item_id,omitempty"`
}

func (c *Core) CreateLocalUploadTask(ctx context.Context, req LocalUploadTaskRequest) (task.Task, error) {
	if len(req.Items) == 0 {
		return task.Task{}, fmt.Errorf("core: local upload task requires at least one item")
	}
	items := make([]task.Item, 0, len(req.Items))
	for _, item := range req.Items {
		if item.LocalPath == "" {
			return task.Task{}, fmt.Errorf("core: local upload task item requires local_path")
		}
		if req.WaitStable {
			if _, err := c.WaitLocalFileStable(ctx, item.LocalPath, req.Stability); err != nil {
				return task.Task{}, err
			}
		}
		items = append(items, task.Item{
			SourcePath: item.LocalPath,
			DestPath:   item.DestPath,
			Path:       item.Path,
			Name:       item.Name,
			ItemID:     item.ItemID,
		})
	}
	taskType := task.TypeUploadRemote
	if len(items) > 1 {
		taskType = task.TypeUploadBatch
	}
	return c.CreateTask(ctx, task.Request{
		Type:    taskType,
		Items:   items,
		Options: req.Options,
	})
}
