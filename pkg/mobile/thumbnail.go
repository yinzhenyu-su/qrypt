package mobile

import "github.com/yinzhenyu/qrypt/pkg/core"

func GetThumbnailFileJSON(coreID, path, preset string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	info, err := withCore(s, func(c *core.Core) (core.ThumbnailInfo, error) { return c.GetThumbnailFile(ctx, path, preset) })
	return resultJSON(info, err)
}

func PutThumbnailFileJSON(coreID, path, preset, mime, localPath string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	info, err := withCore(s, func(c *core.Core) (core.ThumbnailInfo, error) {
		return c.PutThumbnailFile(ctx, path, preset, mime, localPath)
	})
	return resultJSON(info, err)
}

func ThumbnailCacheUsageJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	bytes, err := withCore(s, func(c *core.Core) (int64, error) { return c.ThumbnailCacheUsage(ctx) })
	return resultJSON(map[string]int64{"bytes": bytes}, err)
}

func ClearThumbnailCacheJSON(coreID string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	return resultJSON(nil, withCoreErr(s, func(c *core.Core) error { return c.ClearThumbnailCache(ctx) }))
}
