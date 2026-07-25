package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const uploadCopyChunkSize = 256 * 1024

func (c *Core) UploadLocalFile(ctx context.Context, localPath, remotePath string) (drive.Entry, error) {
	if c == nil || c.fs == nil {
		return drive.Entry{}, fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(localPath) == "" {
		return drive.Entry{}, fmt.Errorf("core: local path required")
	}
	if strings.TrimSpace(remotePath) == "" {
		return drive.Entry{}, fmt.Errorf("core: remote path required")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return drive.Entry{}, err
	}
	defer f.Close()
	if err := c.fs.Create(ctx, remotePath); err != nil {
		return drive.Entry{}, err
	}
	buf := make([]byte, uploadCopyChunkSize)
	var off int64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, err := c.fs.WriteAt(ctx, remotePath, buf[:n], off)
			if err != nil {
				return drive.Entry{}, err
			}
			if written != n {
				return drive.Entry{}, fmt.Errorf("core: short staging write: wrote %d of %d", written, n)
			}
			off += int64(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return drive.Entry{}, readErr
		}
	}
	if err := c.fs.Flush(ctx, remotePath); err != nil {
		return drive.Entry{}, err
	}
	return c.fs.Stat(ctx, remotePath)
}

func (c *Core) BeginStreamingUpload(ctx context.Context, remotePath string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if strings.TrimSpace(remotePath) == "" {
		return fmt.Errorf("core: remote path required")
	}
	return c.fs.Create(ctx, remotePath)
}

func (c *Core) WriteStreamingUpload(ctx context.Context, remotePath string, data []byte, offset int64) (int, error) {
	if c == nil || c.fs == nil {
		return 0, fmt.Errorf("core: closed")
	}
	if offset < 0 {
		return 0, fmt.Errorf("core: offset must be non-negative")
	}
	return c.fs.WriteAt(ctx, remotePath, data, offset)
}

func (c *Core) FinishStreamingUpload(ctx context.Context, remotePath string) (drive.Entry, error) {
	if c == nil || c.fs == nil {
		return drive.Entry{}, fmt.Errorf("core: closed")
	}
	if err := c.fs.Flush(ctx, remotePath); err != nil {
		return drive.Entry{}, err
	}
	return c.fs.Stat(ctx, remotePath)
}

func (c *Core) CancelStreamingUpload(ctx context.Context, remotePath string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	return c.fs.Remove(ctx, remotePath)
}
