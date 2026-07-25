package mobile

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type uploadFinishResult struct {
	Entry entry     `json:"entry"`
	Task  task.Task `json:"task"`
}

func UploadLocalFileJSON(coreID, localPath, remotePath string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.UploadLocalFile(ctx, localPath, remotePath)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	result, err := uploadResultForPath(s.core, remotePath, fromDriveEntry(item, remotePath))
	return resultJSON(result, err)
}

func OpenStreamingUpload(coreID, remotePath string, timeoutMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	if err := s.core.BeginStreamingUpload(ctx, remotePath); err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.uploads[id] = &streamingUploadHandle{coreID: coreID, path: remotePath}
	registry.mu.Unlock()
	return id, nil
}

func OpenStreamingUploadJSON(coreID, remotePath string, timeoutMS int) string {
	id, err := OpenStreamingUpload(coreID, remotePath, timeoutMS)
	return resultJSON(id, err)
}

func WriteStreamingUpload(handleID string, data []byte, timeoutMS int) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	handle, err := getStreamingUpload(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return 0, wrapError(err)
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return 0, wrapError(fmt.Errorf("mobile: streaming upload handle %q is closed", handleID))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	n, err := s.core.WriteStreamingUpload(ctx, handle.path, data, handle.offset)
	if err != nil {
		return n, wrapError(err)
	}
	handle.offset += int64(n)
	return n, nil
}

func WriteStreamingUploadJSON(handleID string, data []byte, timeoutMS int) string {
	n, err := WriteStreamingUpload(handleID, data, timeoutMS)
	return resultJSON(n, err)
}

func FinishStreamingUpload(handleID string, timeoutMS int) (string, error) {
	handle, err := getStreamingUpload(handleID)
	if err != nil {
		return "", wrapError(err)
	}
	handle.mu.Lock()
	if handle.closed {
		handle.mu.Unlock()
		return "", wrapError(fmt.Errorf("mobile: streaming upload handle %q is closed", handleID))
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		handle.mu.Unlock()
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	item, err := s.core.FinishStreamingUpload(ctx, handle.path)
	if err != nil {
		handle.mu.Unlock()
		return "", wrapError(err)
	}
	handle.closed = true
	handle.mu.Unlock()
	removeStreamingUpload(handleID, handle)
	result, err := uploadResultForPath(s.core, handle.path, fromDriveEntry(item, handle.path))
	if err != nil {
		return "", wrapError(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", wrapError(err)
	}
	return string(data), nil
}

func FinishStreamingUploadJSON(handleID string, timeoutMS int) string {
	raw, err := FinishStreamingUpload(handleID, timeoutMS)
	return rawResultJSON(raw, err)
}

func CancelStreamingUpload(handleID string, timeoutMS int) error {
	handle, err := takeStreamingUpload(handleID)
	if err != nil {
		return wrapError(err)
	}
	handle.mu.Lock()
	if handle.closed {
		handle.mu.Unlock()
		return wrapError(fmt.Errorf("mobile: streaming upload handle %q is closed", handleID))
	}
	handle.closed = true
	handle.mu.Unlock()
	s, err := getSession(handle.coreID)
	if err != nil {
		return wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return wrapError(s.core.CancelStreamingUpload(ctx, handle.path))
}

func CancelStreamingUploadJSON(handleID string, timeoutMS int) string {
	return resultJSON(nil, CancelStreamingUpload(handleID, timeoutMS))
}

func uploadResultForPath(c *core.Core, path string, item entry) (uploadFinishResult, error) {
	tasks, err := c.ListTasks(context.Background(), task.Filter{
		Types: []task.Type{task.TypeUploadRemote},
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
