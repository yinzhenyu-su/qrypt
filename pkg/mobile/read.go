package mobile

import (
	"encoding/json"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

type openFileOptions struct {
	DeadlineMS int    `json:"deadline_ms"`
	Priority   string `json:"priority"`
}

func openFileWithPriority(coreID, path string, priority vfs.ReadPriority, deadlineMS int) (string, error) {
	s, err := getSession(coreID)
	if err != nil {
		return "", wrapError(err)
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	item, err := withCore(s, func(c *core.Core) (drive.Entry, error) { return c.Stat(ctx, path) })
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

func OpenFileJSON(coreID, path, optionsRaw string) string {
	options, err := parseOpenFileOptions(optionsRaw)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	priority, err := options.priority()
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := openFileWithPriority(coreID, path, priority, options.DeadlineMS)
	return resultJSON(id, err)
}

func parseOpenFileOptions(raw string) (openFileOptions, error) {
	if raw == "" {
		return openFileOptions{}, nil
	}
	var options openFileOptions
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return openFileOptions{}, err
	}
	return options, nil
}

func (o openFileOptions) priority() (vfs.ReadPriority, error) {
	switch o.Priority {
	case "", "normal":
		return vfs.PriorityNormal, nil
	case "high":
		return vfs.PriorityHigh, nil
	default:
		return vfs.PriorityNormal, fmt.Errorf("mobile: unknown file priority %q", o.Priority)
	}
}

func ReadAtInto(handleID string, offset int64, dst []byte, deadlineMS int) (int, error) {
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
	ctx, done := handle.reads.begin(deadlineMS)
	defer done()
	ctx = vfs.WithReadPriority(ctx, handle.readPriority)
	n, err := withCore(s, func(c *core.Core) (int, error) { return c.ReadAtInto(ctx, handle.path, offset, dst, 0) })
	if err != nil {
		return n, wrapError(err)
	}
	return n, nil
}

// CancelFileReadJSON aborts any in-flight reads on the handle. The handle
// remains usable; future reads are unaffected.
func CancelFileReadJSON(handleID string) string {
	handle, err := getFile(handleID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	handle.reads.cancelAll()
	return resultJSON(nil, nil)
}

func closeFile(handleID string) error {
	registry.mu.Lock()
	handle, ok := registry.files[handleID]
	if !ok {
		registry.mu.Unlock()
		return wrapError(fmt.Errorf("mobile: unknown file handle %q", handleID))
	}
	delete(registry.files, handleID)
	registry.mu.Unlock()
	handle.reads.cancelAll()
	return nil
}

func CloseFileJSON(handleID string) string {
	return resultJSON(nil, closeFile(handleID))
}
