package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func commandUsageError(cmd *cobra.Command, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return fmt.Errorf("%s\n\nUsage:\n  %s", message, cmd.UseLine())
}

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return commandUsageError(cmd, "unexpected argument %q", args[0])
	}
	return nil
}

func commandGroupArgs(hints map[string]string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if hint := hints[args[0]]; hint != "" {
			return fmt.Errorf("%s", hint)
		}
		return fmt.Errorf("unknown command %q for %q\n\nRun '%s --help' to see available commands.", args[0], cmd.CommandPath(), cmd.CommandPath())
	}
}

func exactNamedArgs(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == len(names) {
			return nil
		}
		switch {
		case len(args) < len(names):
			return commandUsageError(cmd, "missing %s", strings.Join(names[len(args):], " and "))
		default:
			return commandUsageError(cmd, "too many arguments: %s", strings.Join(args[len(names):], " "))
		}
	}
}

func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) <= n {
			return nil
		}
		return commandUsageError(cmd, "too many arguments: %s", strings.Join(args[n:], " "))
	}
}

func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

func installFlagErrorHelp(cmd *cobra.Command) {
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\n\nRun '%s --help' for valid flags.", err, cmd.CommandPath())
	})
	for _, child := range cmd.Commands() {
		installFlagErrorHelp(child)
	}
}

func missingSocketError(cmd *cobra.Command) error {
	return fmt.Errorf("--socket PATH is required for runtime debug commands\n\nStart qrypt with a debug socket first:\n  qrypt mount --socket /tmp/qrypt.sock\n\nThen retry:\n  %s --socket /tmp/qrypt.sock", cmd.CommandPath())
}
