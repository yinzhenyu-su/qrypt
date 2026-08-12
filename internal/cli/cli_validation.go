package cli

import (
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func nonNegativeIntFlag(cmd *cobra.Command, name string) (int, error) {
	return cliruntime.NonNegativeIntFlag(cmd, name)
}

func validateSamplingWindow(duration, interval time.Duration, durationFlag, intervalFlag string) error {
	return cliruntime.ValidateSamplingWindow(duration, interval, durationFlag, intervalFlag)
}
