package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// usageNextStep returns the runnable command a usage error points a user at.
// It is always a real command line the user can copy, never a bare "see --help".
func usageNextStep(cmd *cobra.Command) string {
	return cmd.CommandPath() + " --help"
}

// usageError builds a usage error that ends with a concrete next-step command,
// so every usage failure teaches the user how to proceed. It prints the command
// synopsis, never the full usage block.
func usageError(cmd *cobra.Command, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return usageErrorf("%s\n\nUsage:\n  %s\n\nNext:\n  %s", message, cmd.UseLine(), usageNextStep(cmd))
}

// commandUsageError is the single builder behind the runtime's UsageError, so
// the next-step rule cannot drift between call sites.
func commandUsageError(cmd *cobra.Command, format string, args ...any) error {
	return usageError(cmd, format, args...)
}

// unknownCommandMessage reports an unknown subcommand and, when the name is a
// near miss, suggests the intended one.
func unknownCommandMessage(cmd *cobra.Command, name string) string {
	message := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(name); len(suggestions) > 0 {
		message += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t")
	}
	return message
}

func commandGroupArgs(hints map[string]string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		if hint := hints[args[0]]; hint != "" {
			return usageError(cmd, "%s", hint)
		}
		return usageError(cmd, "%s", unknownCommandMessage(cmd, args[0]))
	}
}

// installHelpDefaults wires the shared help behavior into every command: a
// usage error ends with a runnable next step, and a near-miss subcommand is
// suggested. It recurses so subcommands get the same defaults as the root.
func installHelpDefaults(cmd *cobra.Command) {
	cmd.SuggestionsMinimumDistance = 2
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError(cmd, "%s", err.Error())
	})
	for _, child := range cmd.Commands() {
		installHelpDefaults(child)
	}
}
