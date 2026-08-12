package cli

import (
	"github.com/spf13/cobra"
	clidriver "github.com/yinzhenyu/qrypt/internal/cli/driver"
	cliversion "github.com/yinzhenyu/qrypt/internal/cli/version"
)

func newDriverCmd() *cobra.Command {
	return clidriver.NewCommand(cliRuntime{})
}

func newDriverListCmd() *cobra.Command {
	return clidriver.NewListCmd(cliRuntime{})
}

func newDriverSchemaCmd() *cobra.Command {
	return clidriver.NewSchemaCmd(cliRuntime{})
}

func newVersionCmd(info buildInfo) *cobra.Command {
	return cliversion.NewCommand(cliRuntime{}, info)
}
