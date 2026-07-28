package mobile

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

func DebugSnapshotJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	raw, err := s.core.DebugSnapshotJSON(context.Background())
	return rawResultJSON(raw, err)
}

func FlushReadCacheJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.FlushReadCache())
}

func StorageUsageJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	usage, err := s.core.StorageUsage(context.Background())
	return resultJSON(usage, err)
}

func ClearReadCacheJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.ClearReadCache(ctx))
}

func StartDebugServerJSON(coreID, listen string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.StartDebugServer(context.Background(), listen))
}

func StopDebugServerJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	return resultJSON(nil, s.core.StopDebugServer(context.Background()))
}

func LogFilesJSON(coreID string) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	files, err := s.core.LogFiles()
	return resultJSON(files, err)
}

func ReadLogJSON(coreID, name string, offset int64, length int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	data, err := s.core.ReadLog(name, offset, length)
	return resultJSON(data, err)
}
