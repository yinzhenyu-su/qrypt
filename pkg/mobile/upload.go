package mobile

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type uploadFinishResult struct {
	Entry entry     `json:"entry"`
	Task  task.Task `json:"task"`
}

func UploadLocalFileJSON(coreID, localPath, remotePath string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	resolvedRemotePath, err := s.core.ResolveUploadDestination(remotePath, filepath.Base(localPath))
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	item, err := s.core.UploadLocalFile(ctx, localPath, remotePath)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	result, err := uploadResultForPath(ctx, s.core, resolvedRemotePath, fromDriveEntry(item, resolvedRemotePath))
	return resultJSON(result, err)
}

func uploadResultForPath(ctx context.Context, c *core.Core, path string, item entry) (uploadFinishResult, error) {
	tasks, err := c.ListTasks(ctx, task.Filter{
		Types: allUploadTaskTypes,
		Path:  path,
		Limit: 1,
	})
	if err != nil {
		return uploadFinishResult{}, err
	}
	if len(tasks) == 0 {
		return uploadFinishResult{}, fmt.Errorf("mobile: upload task not found for %s", path)
	}
	return uploadFinishResult{Entry: item, Task: tasks[0]}, nil
}
