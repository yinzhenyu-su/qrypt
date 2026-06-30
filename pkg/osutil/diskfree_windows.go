//go:build windows

package osutil

import (
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// DiskFree returns the total and available bytes on the filesystem containing path.
func DiskFree(path string) (total, free int64, err error) {
	root, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return 0, 0, e
	}
	var avail, totalBytes, totalFree int64
	ret, _, _ := getDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&avail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, 0, syscall.GetLastError()
	}
	return totalBytes, avail, nil
}
