package cli

import (
	"github.com/spf13/cobra"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
	"github.com/yinzhenyu/qrypt/pkg/config"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
)

func resolveCheckTarget(cfg *config.Config, arg string) syncer.Target {
	return clifs.ResolveCheckTarget(cfg, arg)
}

func runCheck(cmd *cobra.Command, args []string) error {
	return clifs.NewCheckCmd(cliRuntime{}).RunE(cmd, args)
}

func runFsSync(cmd *cobra.Command, args []string) error {
	return clifs.NewSyncCmd(cliRuntime{}).RunE(cmd, args)
}

func finishSync(cmd *cobra.Command, result syncer.Result, conflictPolicy string) error {
	return clifs.FinishSync(cliRuntime{}, cmd, result, conflictPolicy)
}
