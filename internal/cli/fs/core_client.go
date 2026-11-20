package fs

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/fsclient"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// coreFSRequired reports whether fs data-plane commands must run through the
// resident Core. The CLI test suite pins QRYPT_CORE_FS_REQUIRED=0 to exercise
// the legacy in-process path; production defaults to the single owner Core.
func coreFSRequired() bool {
	return os.Getenv("QRYPT_CORE_FS_REQUIRED") != "0"
}

// openFS opens the filesystem for a command: through the resident Core in
// production, or through the legacy in-process VFS when the test suite pins
// QRYPT_CORE_FS_REQUIRED=0.
func openFS(rt cliruntime.Runtime, cmd *cobra.Command) (context.Context, vfs.FileSystem, func(), error) {
	if coreFSRequired() {
		return openCoreFS(rt, cmd)
	}
	return rt.OpenFileSystem(cmd)
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
	return ctx, fsclient.New(client), stop, nil
}

// waitPathSettled waits for one path's upload/delete work to finish. Core
// clients support per-path waits; the legacy in-process test path falls back to
// the whole-filesystem idle wait.
func waitPathSettled(ctx context.Context, rt cliruntime.Runtime, fs vfs.FileSystem, path string, timeout time.Duration) error {
	if waiter, ok := fs.(interface {
		WaitPath(context.Context, string, time.Duration) error
	}); ok {
		return waiter.WaitPath(ctx, path, timeout)
	}
	opened, ok := fs.(cliruntime.OpenedFileSystem)
	if !ok {
		return fmt.Errorf("filesystem does not expose upload durability")
	}
	return rt.WaitFileSystemIdle(ctx, opened, timeout)
}
