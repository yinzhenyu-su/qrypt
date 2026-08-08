package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfs"
)

func (c *Core) DebugSnapshotJSON(ctx context.Context) (string, error) {
	if c == nil || c.fs == nil {
		return "", fmt.Errorf("core: closed")
	}
	snapshotter, ok := c.fs.(vfs.DebugSnapshotProvider)
	if !ok {
		return "", fmt.Errorf("core: debug snapshot unavailable")
	}
	return marshalJSON(snapshotter.DebugSnapshot())
}

func (c *Core) FlushReadCache() error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	flusher, ok := c.fs.(vfs.ReadCacheFlusher)
	if !ok {
		return fmt.Errorf("core: read cache flush unavailable")
	}
	return flusher.FlushReadCache()
}

func (c *Core) StartDebugServer(ctx context.Context, listen string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if c.debugServer != nil {
		return fmt.Errorf("core: debug server already started")
	}
	snapshotter, ok := c.fs.(control.Snapshotter)
	if !ok {
		return fmt.Errorf("core: debug server requires filesystem debug snapshots")
	}
	server, err := control.NewServer(listen, snapshotter)
	if err != nil {
		return err
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	c.debugServer = server
	return nil
}

func (c *Core) StopDebugServer(ctx context.Context) error {
	if c == nil || c.debugServer == nil {
		return nil
	}
	err := c.debugServer.Close(ctx)
	c.debugServer = nil
	return err
}
