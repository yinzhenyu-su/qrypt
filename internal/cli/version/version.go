package version

import (
	"fmt"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	buildinfo "github.com/yinzhenyu/qrypt/pkg/buildinfo"
)

type BuildInfo = buildinfo.Info

func NewCommand(rt cliruntime.Runtime, info BuildInfo) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show build version information",
		Args:  cliruntime.NoArgs(rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), info)
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
