//go:build windows

package qryptstorage

import (
	"os"
	"syscall"
)

// killSelf terminates the current process with no deferred cleanup, the
// Windows equivalent of SIGKILL for the durability test.
func killSelf() {
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		os.Exit(137)
	}
	_ = syscall.TerminateProcess(handle, 1)
	select {}
}
