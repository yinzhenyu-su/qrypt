package mobile

import (
	"encoding/json"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type openFileOptions struct {
	DeadlineMS int    `json:"deadline_ms"`
	Priority   string `json:"priority"`
	Intent     string `json:"intent"`
}

func openFileWithPriority(coreID, path string, priority core.ReadPriority, intent core.ReadIntent, deadlineMS int) (string, error) {
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
	registry.files[id] = &fileHandle{coreID: coreID, path: path, size: item.Size, readPriority: priority, readIntent: intent}
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
	intent, err := options.intent()
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	id, err := openFileWithPriority(coreID, path, priority, intent, options.DeadlineMS)
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

func (o openFileOptions) priority() (core.ReadPriority, error) {
	switch o.Priority {
	case "", "normal":
		return core.PriorityNormal, nil
	case "high":
		return core.PriorityHigh, nil
	default:
		return core.PriorityNormal, fmt.Errorf("mobile: unknown file priority %q", o.Priority)
	}
}

func (o openFileOptions) intent() (core.ReadIntent, error) {
	switch o.Intent {
	case "", "interactive":
		return core.ReadIntentInteractive, nil
	case "bulk":
		return core.ReadIntentBulk, nil
	case "probe":
		return core.ReadIntentProbe, nil
	default:
		return core.ReadIntentInteractive, fmt.Errorf("mobile: unknown read intent %q", o.Intent)
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
	ctx = core.WithReadIntent(ctx, handle.readIntent)
	n, err := withCore(s, func(c *core.Core) (int, error) {
		return c.ReadAtIntoWithPriority(ctx, handle.path, offset, dst, 0, handle.readPriority)
	})
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
	cancelFilePrefetch(handle)
	return resultJSON(nil, nil)
}

// cancelFilePrefetch stops speculative prefetch for a closed or canceled
// handle. It is best-effort: a deleted file or a filesystem without the
// optional surface must not make the handle operation fail.
func cancelFilePrefetch(handle *fileHandle) {
	if handle == nil {
		return
	}
	s, err := getSession(handle.coreID)
	if err != nil {
		return
	}
	ctx, cancel := s.timeoutContext(0)
	defer cancel()
	_ = withCoreErr(s, func(c *core.Core) error {
		return c.CancelReadPrefetch(ctx, handle.path)
	})
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
	cancelFilePrefetch(handle)
	return nil
}

func CloseFileJSON(handleID string) string {
	return resultJSON(nil, closeFile(handleID))
}
