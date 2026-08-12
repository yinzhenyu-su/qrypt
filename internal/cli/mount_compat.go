package cli

import (
	"github.com/spf13/cobra"
	climount "github.com/yinzhenyu/qrypt/internal/cli/mount"
)

func newMountCmd() *cobra.Command {
	return climount.NewCommand(cliRuntime{})
}
