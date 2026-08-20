package cli

import buildinfo "github.com/yinzhenyu/qrypt/pkg/buildinfo"

type buildInfo = buildinfo.Info

func currentBuildInfo() buildInfo {
	return buildinfo.Current()
}
