package core

import (
	"context"
	"fmt"
	"io"

	"github.com/yinzhenyu/qrypt/pkg/limits"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

const DefaultReadChunkLimit = limits.DefaultReadRequestBytes

// Read intent is re-exported so mobile clients can choose a strategy without
// importing pkg/vfs directly (the mobile package is intentionally below it).
type ReadIntent = vfs.ReadIntent

const (
	ReadIntentInteractive = vfs.ReadIntentInteractive
	ReadIntentBulk        = vfs.ReadIntentBulk
	ReadIntentProbe       = vfs.ReadIntentProbe
)

var WithReadIntent = vfs.WithReadIntent

func (c *Core) readLimit(limit int) int {
	if limit > 0 {
		return limit
	}
	if c != nil && c.readChunkLimit > 0 {
		return c.readChunkLimit
	}
	return DefaultReadChunkLimit
}

func (c *Core) Read(ctx context.Context, path string, offset, size int64) (io.ReadCloser, error) {
	if c == nil || c.fs == nil {
		return nil, fmt.Errorf("core: closed")
	}
	return c.fs.Read(ctx, path, offset, size)
}

// CancelReadPrefetch stops speculative prefetch for path without dropping
// completed cache fills. It is best-effort for filesystems that do not expose
// the optional surface.
func (c *Core) CancelReadPrefetch(ctx context.Context, path string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if canceller, ok := c.fs.(interface {
		CancelReadPrefetch(context.Context, string) error
	}); ok {
		return canceller.CancelReadPrefetch(ctx, path)
	}
	return nil
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
	limit = c.readLimit(limit)
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
	limit = c.readLimit(limit)
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

// ReadAtIntoWithPriority reads into dst like ReadAtInto, with a client
// ReadPriority mapped to the VFS read scheduler inside core. Under slot
// contention the scheduler honors PriorityHigh reads ahead of lower-priority
// work; clients never paint scheduling values onto a context themselves.
func (c *Core) ReadAtIntoWithPriority(ctx context.Context, path string, offset int64, dst []byte, limit int, priority ReadPriority) (int, error) {
	if priority < PriorityLow || priority > PriorityHigh {
		return 0, fmt.Errorf("core: unknown read priority %d", priority)
	}
	readCtx := vfs.WithReadPriority(ctx, priorityToVFS(priority))
	return c.ReadAtInto(readCtx, path, offset, dst, limit)
}
