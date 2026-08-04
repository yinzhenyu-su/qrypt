package mobile

import (
	"context"
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

func DebugSnapshotJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	raw, err := withCore(s, func(c *core.Core) (string, error) { return c.DebugSnapshotJSON(context.Background()) })
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return resultJSON(nil, wrapError(err))
	}
	snapshot["mobile_handles"] = mobileHandleCounts(coreID)
	return resultJSON(snapshot, nil)
}

func mobileHandleCounts(coreID string) map[string]int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	counts := map[string]int{}
	for _, handle := range registry.files {
		if handle.coreID == coreID {
			counts["files"]++
		}
	}
	for _, handle := range registry.virtuals {
		if handle.coreID == coreID {
			counts["virtuals"]++
		}
	}
	for _, handle := range registry.downloads {
		if handle.coreID == coreID {
			counts["downloads"]++
		}
	}
	for _, handle := range registry.taskUploads {
		if handle.coreID == coreID {
			counts["task_uploads"]++
		}
	}
	for _, handle := range registry.taskEvents {
		if handle.coreID == coreID {
			counts["task_events"]++
		}
	}
	return counts
}

func FlushReadCacheJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.FlushReadCache() }))
}

func StorageUsageJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	usage, err := withCore(s, func(c *core.Core) (core.StorageUsage, error) { return c.StorageUsage(context.Background()) })
	return resultJSON(usage, err)
}

func ClearReadCacheJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.ClearReadCache(ctx) }))
}

func StartDebugServerJSON(coreID, listen string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.StartDebugServer(context.Background(), listen) }))
}

func StopDebugServerJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.StopDebugServer(context.Background()) }))
}

func LogFilesJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	files, err := withCore(s, func(c *core.Core) ([]core.LogFile, error) { return c.LogFiles() })
	return resultJSON(files, err)
}

func ReadLogJSON(coreID, name string, offset int64, length int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	data, err := withCore(s, func(c *core.Core) ([]byte, error) { return c.ReadLog(name, offset, length) })
	return resultJSON(data, err)
}
