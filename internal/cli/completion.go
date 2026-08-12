package cli

import (
	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func noFileCompletions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return cliruntime.NoFileCompletions(cmd, args, toComplete)
}
