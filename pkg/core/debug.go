package core

import (
	"context"
	"fmt"

	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

func (c *Core) DebugSnapshotJSON(ctx context.Context) (string, error) {
	if c == nil || c.fs == nil {
		return "", fmt.Errorf("core: closed")
	}
	snapshotter, ok := c.fs.(diagnostics.DebugSnapshotProvider)
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
	if !vfs.HasCapability(c.fs, vfs.CapabilityFlushReadCache) || !ok {
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
	server.SetTaskPersistenceHealth(c)
	server.SetTaskDebugger(c)
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

// StartControlServer starts the mutable local command surface. It is kept
// separate from the diagnostics server so a debug endpoint can never gain
// mutation authority by accident.
func (c *Core) StartControlServer(ctx context.Context, listen string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if c.controlServer != nil {
		return fmt.Errorf("core: control server already started")
	}
	server, err := control.NewCommandServer(listen, c)
	if err != nil {
		return err
	}
	server.SetOfflineService(c)
	if err := server.Start(ctx); err != nil {
		return err
	}
	c.controlServer = server
	return nil
}

func (c *Core) StopControlServer(ctx context.Context) error {
	if c == nil || c.controlServer == nil {
		return nil
	}
	err := c.controlServer.Close(ctx)
	c.controlServer = nil
	return err
}

// StartProbeServer starts the active test and benchmark surface on its own
// local endpoint, separate from both read-only diagnostics and task control.
func (c *Core) StartProbeServer(ctx context.Context, listen string) error {
	if c == nil || c.fs == nil {
		return fmt.Errorf("core: closed")
	}
	if c.probeServer != nil {
		return fmt.Errorf("core: probe server already started")
	}
	snapshotter, ok := c.fs.(control.Snapshotter)
	if !ok {
		return fmt.Errorf("core: probe server requires filesystem debug snapshots")
	}
	server, err := control.NewProbeServer(listen, snapshotter)
	if err != nil {
		return err
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	c.probeServer = server
	return nil
}

func (c *Core) StopProbeServer(ctx context.Context) error {
	if c == nil || c.probeServer == nil {
		return nil
	}
	err := c.probeServer.Close(ctx)
	c.probeServer = nil
	return err
}
