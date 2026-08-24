package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/media"
)

func (c *Core) ProbeMP4(ctx context.Context, path string) (media.MP4Probe, error) {
	item, err := c.Stat(ctx, path)
	if err != nil {
		return media.MP4Probe{}, err
	}
	if item.IsDir {
		return media.MP4Probe{}, fmt.Errorf("core: %s is a directory", path)
	}
	return media.ProbeMP4(ctx, item.Size, func(ctx context.Context, offset int64, length int) ([]byte, error) {
		return c.readAtRange(ctx, path, offset, length)
	})
}

func (c *Core) OpenVirtualFile(ctx context.Context, path, mode string) (media.VirtualFile, error) {
	item, err := c.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if item.IsDir {
		return nil, fmt.Errorf("core: %s is a directory", path)
	}
	readAt := func(ctx context.Context, offset int64, length int) ([]byte, error) {
		return c.readAtRange(ctx, path, offset, length)
	}
	readAtInto := func(ctx context.Context, offset int64, dst []byte) (int, error) {
		return c.ReadAtInto(ctx, path, offset, dst, 0)
	}
	return media.NewVirtualFileInto(ctx, mode, item.Size, readAt, readAtInto)
}

func (c *Core) readAtRange(ctx context.Context, path string, offset int64, length int) ([]byte, error) {
	if length <= 0 {
		return []byte{}, nil
	}
	limit := c.readLimit(0)
	data := make([]byte, 0, length)
	for len(data) < length {
		want := min(length-len(data), limit)
		chunk, err := c.ReadAt(ctx, path, offset+int64(len(data)), want, limit)
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			break
		}
		data = append(data, chunk...)
		if len(chunk) < want {
			break
		}
	}
	return data, nil
}
