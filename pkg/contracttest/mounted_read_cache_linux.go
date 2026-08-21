//go:build linux

package contracttest

import (
	"os"

	"golang.org/x/sys/unix"
)

func prepareColdMountedRead(file *os.File) string {
	if err := unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED); err != nil {
		return "fadvise_dontneed_failed"
	}
	return "fadvise_dontneed"
}
