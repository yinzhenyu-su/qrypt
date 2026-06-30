//go:build !windows

package localfs

import (
	"context"
	"fmt"
	"syscall"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(d.root, &stat); err != nil {
		return drive.Space{}, fmt.Errorf("localfs: statfs root: %w", err)
	}
	blockSize := int64(stat.Bsize)
	return drive.Space{
		Total: int64(stat.Blocks) * blockSize,
		Free:  int64(stat.Bavail) * blockSize,
	}, nil
}
