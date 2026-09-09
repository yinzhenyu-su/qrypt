package core

import (
	"context"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (c *Core) UploadLocalFile(ctx context.Context, localPath, remotePath string) (drive.Entry, error) {
	service, err := c.UploadService()
	if err != nil {
		return drive.Entry{}, err
	}
	return service.UploadLocalFile(ctx, UploadLocalFileRequest{LocalPath: localPath, DestPath: remotePath})
}
