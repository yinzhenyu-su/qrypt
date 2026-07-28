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
	ctx, cancel := core.TimeoutContext(deadlineMS)
	defer cancel()
	result, err := s.core.WaitLocalFileStable(ctx, localPath, opts)
	return resultJSON(result, err)
}
