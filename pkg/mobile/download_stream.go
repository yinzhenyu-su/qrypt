package mobile

import "github.com/yinzhenyu/qrypt/pkg/core"

func openTaskDownloadItem(coreID, taskID, itemID string, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	handle, err := s.core.OpenDownloadStreamItem(ctx, taskID, itemID)
	if err != nil {
		return "", wrapError(err)
	}
	id, err := newID()
	if err != nil {
		_ = handle.Close()
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.downloads[id] = &downloadStreamHandle{coreID: coreID, handle: handle}
	registry.mu.Unlock()
	return id, nil
}

func OpenDownloadItemJSON(coreID, taskID, itemID string, deadlineMS int) string {
	id, err := openTaskDownloadItem(coreID, taskID, itemID, deadlineMS)
	return resultJSON(id, err)
}

func ReadDownloadItemInto(handleID string, dst []byte, deadlineMS int) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getDownloadStream(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	n, err := handle.handle.ReadInto(ctx, dst)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func AckDownloadItemJSON(handleID string, bytesWritten int64) string {
	handle, err := getDownloadStream(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, handle.handle.Ack(bytesWritten))
}

func CommitDownloadItemJSON(handleID string, deadlineMS int) string {
	handle, err := takeDownloadStream(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, handle.handle.Commit(ctx))
}

func FailDownloadItemJSON(handleID, code, message string) string {
	handle, err := takeDownloadStream(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, handle.handle.Fail(code, message))
}

func PauseDownloadItemJSON(handleID string) string {
	handle, err := takeDownloadStream(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, handle.handle.Close())
}
