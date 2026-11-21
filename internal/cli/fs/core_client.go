package fs

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/fsclient"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// openFS opens the filesystem for a command through the resident Core.
func openFS(rt cliruntime.Runtime, cmd *cobra.Command) (context.Context, vfs.FileSystem, func(), error) {
	return openCoreFS(rt, cmd)
}

// openCoreFS connects to (or lazily starts) the resident Core and returns a
// FileSystem backed by its data plane.
func openCoreFS(rt cliruntime.Runtime, cmd *cobra.Command) (context.Context, *fsclient.FS, func(), error) {
	state, err := rt.CommandConfig(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	if state.Cfg == nil {
		return nil, nil, nil, rt.ConfigNotFoundError()
	}
	ctx, stop := rt.ShutdownContext(cmd)
	client, err := clicore.EnsureClient(ctx, rt, state)
	if err != nil {
		stop()
		return nil, nil, nil, fmt.Errorf("filesystem operations require the resident Core: %w", err)
	}
	return ctx, fsclient.New(client), func() {
		client.CloseIdleConnections()
		stop()
	}, nil
}

// waitFilesystemSettled waits for all upload/delete work in the shared runtime.
func waitFilesystemSettled(ctx context.Context, fs vfs.FileSystem, timeout time.Duration) error {
	waiter, ok := fs.(interface {
		WaitIdle(context.Context, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("filesystem does not expose upload durability")
	}
	return waiter.WaitIdle(ctx, timeout)
}

// waitPathSettled waits for one path's upload/delete work to finish. Core
// clients support per-path waits; the legacy in-process test path falls back to
// the whole-filesystem idle wait.
func waitPathSettled(ctx context.Context, fs vfs.FileSystem, path string, timeout time.Duration) error {
	waiter, ok := fs.(interface {
		WaitPath(context.Context, string, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("filesystem does not expose upload durability")
	}
	return waiter.WaitPath(ctx, path, timeout)
}
