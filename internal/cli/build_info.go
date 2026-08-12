package cli

import (
	"runtime"
	"runtime/debug"
	"strconv"

	cliversion "github.com/yinzhenyu/qrypt/internal/cli/version"
)

type buildInfo = cliversion.BuildInfo

var (
	buildVersion = ""
	buildCommit  = ""
	buildTime    = ""
	buildDirty   = ""
)

func currentBuildInfo() buildInfo {
	info := buildInfo{
		Version:   "dev",
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if buildVersion != "" {
		info.Version = buildVersion
	}
	if buildCommit != "" {
		info.Commit = buildCommit
	}
	if buildTime != "" {
		info.BuildTime = buildTime
	}
	if dirty, err := strconv.ParseBool(buildDirty); err == nil {
		info.Dirty = dirty
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = build.Main.Version
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = setting.Value
			}
		case "vcs.time":
			if info.BuildTime == "" {
				info.BuildTime = setting.Value
			}
		case "vcs.modified":
			if buildDirty == "" {
				info.Dirty = setting.Value == "true"
			}
		}
	}
	return info
}
