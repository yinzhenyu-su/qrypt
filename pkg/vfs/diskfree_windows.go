//go:build windows

package vfs

import (
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func diskFreeBytes(path string) (int64, error) {
	root, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree int64
	ret, _, _ := getDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return 0, syscall.GetLastError()
	}
	return free, nil
}
