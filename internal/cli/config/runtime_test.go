package config

import (
	"fmt"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

// newTestRuntime wires a cliruntime.TestRuntime for the config command tests:
// the --config flag reads, config-not-found errors, and usage errors. Methods
// a command under test does not call stay unset and panic, so an unexpected
// dependency fails loudly instead of silently passing.
func newTestRuntime() cliruntime.TestRuntime {
	return cliruntime.TestRuntime{
		CommandConfigPathFn: func(cmd *cobra.Command) (string, error) {
			return cmd.Flags().GetString("config")
		},
		ConfigNotFoundErrorFn: func() error {
			return fmt.Errorf("config not found")
		},
		UsageErrorFn: func(_ *cobra.Command, format string, args ...any) error {
			return fmt.Errorf(format, args...)
		},
		WithConfigFlagFn: func(cmd *cobra.Command) *cobra.Command {
			cmd.Flags().StringP("config", "c", "", "config file path")
			return cmd
		},
	}
}
