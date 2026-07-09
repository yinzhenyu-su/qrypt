package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func nonNegativeIntFlag(cmd *cobra.Command, name string) (int, error) {
	value, err := cmd.Flags().GetInt(name)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("--%s must not be negative", name)
	}
	return value, nil
}

func validateSamplingWindow(duration, interval time.Duration, durationFlag, intervalFlag string) error {
	if duration <= 0 {
		return fmt.Errorf("--%s must be greater than 0", durationFlag)
	}
	if interval <= 0 {
		return fmt.Errorf("--%s must be greater than 0", intervalFlag)
	}
	if interval > duration {
		return fmt.Errorf("--%s must not exceed --%s", intervalFlag, durationFlag)
	}
	return nil
}
