package cli

import (
	"github.com/spf13/cobra"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
)

func newFsCmd() *cobra.Command {
	return clifs.NewCommand(cliRuntime{})
}

func runStat(cmd *cobra.Command, args []string) error {
	return clifs.NewStatCmd(cliRuntime{}).RunE(cmd, args)
}

func runMkdir(cmd *cobra.Command, args []string) error {
	return clifs.NewMkdirCmd(cliRuntime{}).RunE(cmd, args)
}

func runPut(cmd *cobra.Command, args []string) error {
	return clifs.NewPutCmd(cliRuntime{}).RunE(cmd, args)
}

func runRm(cmd *cobra.Command, args []string) error {
	return clifs.NewRmCmd(cliRuntime{}).RunE(cmd, args)
}

func runMv(cmd *cobra.Command, args []string) error {
	return clifs.NewMvCmd(cliRuntime{}).RunE(cmd, args)
}
