//go:build darwin

package contracttest

import (
	"os"

	"golang.org/x/sys/unix"
)

func CurrentProcessRSS() (uint64, string, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return 0, "", false
	}
	pages := int64(kp.Eproc.Xrssize)
	if pages <= 0 {
		return 0, "", false
	}
	return uint64(pages) * uint64(os.Getpagesize()), "kern.proc.pid.xrssize", true
}
