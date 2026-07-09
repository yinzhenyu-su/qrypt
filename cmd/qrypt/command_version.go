package main

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	Dirty     bool   `json:"dirty,omitempty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func currentBuildInfo() buildInfo {
	info := buildInfo{
		Version:   "dev",
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = build.Main.Version
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			info.Commit = setting.Value
		case "vcs.time":
			info.BuildTime = setting.Value
		case "vcs.modified":
			info.Dirty = setting.Value == "true"
		}
	}
	return info
}

func newVersionCmd(info buildInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show build version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "qrypt %s\n", info.Version)
			if info.Commit != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "commit: %s\n", info.Commit)
			}
			if info.BuildTime != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "built: %s\n", info.BuildTime)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "go: %s\nplatform: %s/%s\n", info.GoVersion, info.OS, info.Arch)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}
