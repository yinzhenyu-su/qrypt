//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func shutdownSignals() []os.Signal {
	// SIGHUP is what a closed terminal or a dropped SSH session delivers; without
	// it the process dies before unmounting and leaves a stale FUSE mount behind
	// (pkg/mount recovers from that on the next mount, but shutting down cleanly
	// is free). SIGQUIT is deliberately absent: Go turns it into a goroutine dump,
	// which is worth keeping for debugging a hung mount.
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
