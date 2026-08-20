// Package buildinfo collects qrypt build metadata shared by the CLI and the
// control debug server, so `qrypt version` and /v1/health report the same
// values. The build* variables are injected at link time via -X ldflags.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strconv"
)

// Info mirrors the output of `qrypt version --json`.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

var (
	buildVersion = ""
	buildCommit  = ""
	buildTime    = ""
	buildDirty   = ""
)

// Current resolves build metadata from injected -ldflags values, falling back
// to the Go module VCS build info.
func Current() Info {
	info := Info{
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
