package mobile

import "github.com/yinzhenyu/qrypt/pkg/core"

func GetThumbnailFileJSON(coreID, path, preset string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	info, err := s.core.GetThumbnailFile(ctx, path, preset)
	return resultJSON(info, err)
}

func PutThumbnailFileJSON(coreID, path, preset, mime, localPath string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	info, err := s.core.PutThumbnailFile(ctx, path, preset, mime, localPath)
	return resultJSON(info, err)
}

func ThumbnailCacheUsageJSON(coreID string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	bytes, err := s.core.ThumbnailCacheUsage(ctx)
	return resultJSON(bytes, err)
}

func ClearThumbnailCacheJSON(coreID string, timeoutMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := core.TimeoutContext(timeoutMS)
	defer cancel()
	return resultJSON(nil, s.core.ClearThumbnailCache(ctx))
}
