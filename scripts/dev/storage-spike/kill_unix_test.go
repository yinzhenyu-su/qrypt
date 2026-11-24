//go:build !windows

package qryptstorage

import (
	"os"
	"syscall"
)

func killSelf() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	select {}
}
