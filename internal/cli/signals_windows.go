//go:build windows

package cli

import (
	"os"
)

func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
