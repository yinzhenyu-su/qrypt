package vfs

import "github.com/yinzhenyu/qrypt/pkg/osutil"

func diskFreeBytes(path string) (int64, error) {
	_, free, err := osutil.DiskFree(path)
	return free, err
}
