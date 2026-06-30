//go:build windows

package localfs

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	root, err := syscall.UTF16PtrFromString(d.root)
	if err != nil {
		return drive.Space{}, fmt.Errorf("localfs: root encoding: %w", err)
	}
	var free, total, totalFree int64
	ret, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(root)),
		uintptr(unsafe.Pointer(&free)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ret == 0 {
		return drive.Space{}, fmt.Errorf("localfs: GetDiskFreeSpaceEx failed")
	}
	return drive.Space{
		Total: total,
		Free:  free,
	}, nil
}
