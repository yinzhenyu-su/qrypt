package mobile

import (
	"github.com/yinzhenyu/qrypt/pkg/core"
)

func openTaskUploadItem(coreID, taskID, itemID string, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	handle, err := s.core.OpenUploadStreamItem(ctx, taskID, itemID)
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		_ = handle.Close()
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.taskUploads[id] = &uploadStreamItemHandle{coreID: coreID, handle: handle}
	registry.mu.Unlock()
	return id, nil
}

func OpenUploadItemJSON(coreID, taskID, itemID string, deadlineMS int) string {
	id, err := openTaskUploadItem(coreID, taskID, itemID, deadlineMS)
	return resultJSON(id, err)
}

func WriteUploadItem(handleID string, data []byte, deadlineMS int) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	handle, err := getUploadStreamItem(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	n, err := handle.handle.Write(ctx, data)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func CommitUploadItemJSON(handleID string, deadlineMS int) string {
	handle, err := takeUploadStreamItem(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, handle.handle.Commit(ctx))
}

func FailUploadItemJSON(handleID, code, message string) string {
	handle, err := takeUploadStreamItem(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, handle.handle.Fail(code, message))
}

func PauseUploadItemJSON(handleID string) string {
	handle, err := takeUploadStreamItem(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, handle.handle.Close())
}
