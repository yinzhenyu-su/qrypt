package core

import (
	"context"
	"fmt"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

const DefaultReadChunkLimit = 4 << 20

func (c *Core) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	return c.fs.Read(ctx, path, offset, size)
}

func (c *Core) ReadAt(ctx context.Context, path string, offset int64, length int, limit int) ([]byte, error) {
	if offset < 0 {
		return nil, fmt.Errorf("core: offset must be non-negative")
	}
	if length < 0 {
		return nil, fmt.Errorf("core: length must be non-negative")
	}
	if length == 0 {
		return []byte{}, nil
	}
	if limit <= 0 {
		limit = DefaultReadChunkLimit
	}
	if length > limit {
		return nil, fmt.Errorf("core: read length %d exceeds limit %d", length, limit)
	}
	rc, err := c.Read(ctx, path, offset, int64(length))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (c *Core) ReadAtInto(ctx context.Context, path string, offset int64, dst []byte, limit int) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("core: offset must be non-negative")
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = DefaultReadChunkLimit
	}
	if len(dst) > limit {
		return 0, fmt.Errorf("core: read length %d exceeds limit %d", len(dst), limit)
	}
	rc, err := c.Read(ctx, path, offset, int64(len(dst)))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	n, err := io.ReadFull(rc, dst)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return n, nil
	}
	return n, err
}

func readAtNoPrefetch(c *Core, path string, ctx context.Context, offset int64, length int) ([]byte, error) {
	limit := DefaultReadChunkLimit
	if length > limit {
		limit = length
	}
	return c.ReadAt(vfs.WithoutReadPrefetch(ctx), path, offset, length, limit)
}

func readAtIntoNoPrefetch(c *Core, path string, ctx context.Context, offset int64, dst []byte) (int, error) {
	limit := DefaultReadChunkLimit
	if len(dst) > limit {
		limit = len(dst)
	}
	return c.ReadAtInto(vfs.WithoutReadPrefetch(ctx), path, offset, dst, limit)
}
