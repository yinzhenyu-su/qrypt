//go:build !windows

package osutil

import (
	"syscall"
)

// DiskFree returns the total and available bytes on the filesystem containing path.
func DiskFree(path string) (total, free int64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	blockSize := int64(stat.Bsize)
	return int64(stat.Blocks) * blockSize, int64(stat.Bavail) * blockSize, nil
}
