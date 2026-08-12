package cli

import (
	"github.com/spf13/cobra"
	clifs "github.com/yinzhenyu/qrypt/internal/cli/fs"
)

type fsListEntry = clifs.ListEntry

func runGet(cmd *cobra.Command, args []string) error {
	return clifs.NewGetCmd(cliRuntime{}).RunE(cmd, args)
}
