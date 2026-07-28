package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (c *Core) UploadLocalFile(ctx context.Context, localPath, remotePath string) (drive.Entry, error) {
	service, err := c.UploadService()
	if err != nil {
		return drive.Entry{}, err
	}
	return service.UploadLocalFile(ctx, UploadLocalFileRequest{LocalPath: localPath, DestPath: remotePath})
}

func (c *Core) beginStreamingUpload(ctx context.Context, remotePath string) error {
	service, err := c.UploadService()
	if err != nil {
		return err
	}
	if strings.TrimSpace(remotePath) == "" {
		return fmt.Errorf("core: remote path required")
	}
	return service.BeginStream(ctx, remotePath)
}

func (c *Core) writeStreamingUpload(ctx context.Context, remotePath string, data []byte, offset int64) (int, error) {
	service, err := c.UploadService()
	if err != nil {
		return 0, err
	}
	return service.WriteStream(ctx, remotePath, data, offset)
}

func (c *Core) finishStreamingUpload(ctx context.Context, remotePath string) (drive.Entry, error) {
	service, err := c.UploadService()
	if err != nil {
		return drive.Entry{}, err
	}
	return service.FinishStream(ctx, remotePath)
}

func (c *Core) cancelStreamingUpload(ctx context.Context, remotePath string) error {
	service, err := c.UploadService()
	if err != nil {
		return err
	}
	return service.CancelStream(ctx, remotePath)
}
