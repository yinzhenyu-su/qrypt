//go:build !windows

package cli

import (
	"os"
	"syscall"
	"testing"
)

func TestShutdownSignalsCoverTerminalHangup(t *testing.T) {
	signals := shutdownSignals()
	handled := make(map[os.Signal]bool, len(signals))
	for _, signal := range signals {
		handled[signal] = true
	}

	// A closed terminal or dropped SSH session delivers SIGHUP; unhandled, the
	// process dies before unmounting and leaves a stale FUSE mount behind.
	for _, want := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !handled[want] {
			t.Errorf("shutdownSignals() = %v, missing %v", signals, want)
		}
	}
	// SIGQUIT must stay unhandled: Go turns it into a goroutine dump.
	if handled[syscall.SIGQUIT] {
		t.Errorf("shutdownSignals() = %v, must not intercept SIGQUIT", signals)
	}
}
