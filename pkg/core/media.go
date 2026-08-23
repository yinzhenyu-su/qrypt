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
		return c.ReadAt(ctx, path, offset, length, DefaultReadChunkLimit)
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
		return c.ReadAt(ctx, path, offset, length, DefaultReadChunkLimit)
	}
	readAtInto := func(ctx context.Context, offset int64, dst []byte) (int, error) {
		return c.ReadAtInto(ctx, path, offset, dst, DefaultReadChunkLimit)
	}
	return media.NewVirtualFileInto(ctx, mode, item.Size, readAt, readAtInto)
}
