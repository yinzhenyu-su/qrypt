package mobile

import "github.com/yinzhenyu/qrypt/pkg/core"

func GetThumbnailFileJSON(coreID, path, preset string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	info, err := s.core.GetThumbnailFile(ctx, path, preset)
	return resultJSON(info, err)
}

func PutThumbnailFileJSON(coreID, path, preset, mime, localPath string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	info, err := s.core.PutThumbnailFile(ctx, path, preset, mime, localPath)
	return resultJSON(info, err)
}

func ThumbnailCacheUsageJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	bytes, err := s.core.ThumbnailCacheUsage(ctx)
	return resultJSON(map[string]int64{"bytes": bytes}, err)
}

func ClearThumbnailCacheJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, s.core.ClearThumbnailCache(ctx))
}
