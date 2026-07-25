package mobile

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func OpenFile(coreID, path string) (string, error) {
	return openFileWithPriority(coreID, path, vfs.PriorityNormal)
}

func OpenFileHighPriority(coreID, path string) (string, error) {
	return openFileWithPriority(coreID, path, vfs.PriorityHigh)
}

func openFileWithPriority(coreID, path string, priority vfs.ReadPriority) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	item, err := s.core.Stat(context.Background(), path)
	if err != nil {
		return "", wrapError(err)
	}
	if item.IsDir {
		return "", wrapError(fmt.Errorf("mobile: %s is a directory", path))
	}
	id, err := newID()
	if err != nil {
		return "", wrapError(err)
	}
	registry.mu.Lock()
	registry.files[id] = &fileHandle{coreID: coreID, path: path, size: item.Size, readPriority: priority}
	registry.mu.Unlock()
	return id, nil
}

func OpenFileJSON(coreID, path string) string {
	id, err := OpenFile(coreID, path)
	return resultJSON(id, err)
}

func OpenFileHighPriorityJSON(coreID, path string) string {
	id, err := OpenFileHighPriority(coreID, path)
	return resultJSON(id, err)
}

func ReadAt(handleID string, offset int64, length int) ([]byte, error) {
	return ReadAtWithTimeout(handleID, offset, length, 0)
}

func ReadAtInto(handleID string, offset int64, dst []byte) (int, error) {
	return ReadAtIntoWithTimeout(handleID, offset, dst, 0)
}

func ReadAtWithTimeout(handleID string, offset int64, length int, timeoutMS int) ([]byte, error) {
	if offset < 0 {
		return nil, wrapError(fmt.Errorf("mobile: offset must be non-negative"))
	}
	if length < 0 {
		return nil, wrapError(fmt.Errorf("mobile: length must be non-negative"))
	}
	if length == 0 {
		return []byte{}, nil
	}
	handle, err := getFile(handleID)
	if err != nil {
		return nil, wrapError(err)
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return nil, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	ctx = vfs.WithReadPriority(ctx, handle.readPriority)
	data := make([]byte, length)
	n, err := s.core.ReadAtInto(ctx, handle.path, offset, data, 0)
	if err != nil {
		return nil, wrapError(err)
	}
	return data[:n], nil
}

func ReadAtIntoWithTimeout(handleID string, offset int64, dst []byte, timeoutMS int) (int, error) {
	if offset < 0 {
		return 0, wrapError(fmt.Errorf("mobile: offset must be non-negative"))
	}
	if len(dst) == 0 {
		return 0, nil
	}
	handle, err := getFile(handleID)
	if err != nil {
		return 0, wrapError(err)
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return 0, wrapError(err)
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	ctx = vfs.WithReadPriority(ctx, handle.readPriority)
	n, err := s.core.ReadAtInto(ctx, handle.path, offset, dst, 0)
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

func ReadAtJSON(handleID string, offset int64, length int, timeoutMS int) string {
	data, err := ReadAtWithTimeout(handleID, offset, length, timeoutMS)
	return resultJSON(data, err)
}

func CloseFile(handleID string) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, ok := registry.files[handleID]; !ok {
		return wrapError(fmt.Errorf("mobile: unknown file handle %q", handleID))
	}
	delete(registry.files, handleID)
	return nil
}

func CloseFileJSON(handleID string) string {
	return resultJSON(nil, CloseFile(handleID))
}
