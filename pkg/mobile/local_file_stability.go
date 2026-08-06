package mobile

import (
	"encoding/json"

	"github.com/yinzhenyu/qrypt/pkg/core"
)

func WaitLocalFileStableJSON(coreID, localPath, optionsRaw string, deadlineMS int) string {
	s, err := getSession(coreID)
	if err != nil {
		return resultJSON(nil, wrapError(err))
	}
	var opts core.LocalFileStabilityOptions
	if optionsRaw != "" {
		if err := json.Unmarshal([]byte(optionsRaw), &opts); err != nil {
			return resultJSON(nil, wrapError(err))
		}
	}
	ctx, cancel := s.timeoutContext(deadlineMS)
	defer cancel()
	result, err := withCore(s, func(c *core.Core) (core.LocalFileStability, error) {
		return c.WaitLocalFileStable(ctx, localPath, opts)
	})
	return resultJSON(result, err)
}
