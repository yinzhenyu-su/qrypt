//go:build darwin

package contracttest

import (
	"os"

	"golang.org/x/sys/unix"
)

func prepareColdMountedRead(file *os.File) string {
	if _, err := unix.FcntlInt(file.Fd(), unix.F_NOCACHE, 1); err != nil {
		return "f_nocache_failed"
	}
	return "f_nocache"
}
