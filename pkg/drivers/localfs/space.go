package localfs

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	total, free, err := osutil.DiskFree(d.root)
	if err != nil {
		return drive.Space{}, fmt.Errorf("localfs: diskfree root: %w", err)
	}
	return drive.Space{Total: total, Free: free}, nil
}
