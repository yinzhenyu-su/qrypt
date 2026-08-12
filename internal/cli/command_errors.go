package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func commandUsageError(cmd *cobra.Command, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return usageErrorf("%s\n\nUsage:\n  %s", message, cmd.UseLine())
}

func commandGroupArgs(hints map[string]string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if hint := hints[args[0]]; hint != "" {
			return usageErrorf("%s", hint)
		}
		return usageErrorf("unknown command %q for %q\n\nRun '%s --help' to see available commands.", args[0], cmd.CommandPath(), cmd.CommandPath())
	}
}

func installFlagErrorHelp(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageErrorf("%w\n\nRun '%s --help' for valid flags.", err, cmd.CommandPath())
	})
	for _, child := range cmd.Commands() {
		installFlagErrorHelp(child)
	}
}

func missingSocketError(cmd *cobra.Command) error {
	return fmt.Errorf("--socket PATH is required for runtime debug commands\n\nStart qrypt with a debug socket first:\n  qrypt mount --socket /tmp/qrypt.sock\n\nThen retry:\n  %s --socket /tmp/qrypt.sock", cmd.CommandPath())
}
