package fs

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
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
	if err := clicore.EnsureCore(ctx, rt, state); err != nil {
		stop()
		return nil, nil, nil, fmt.Errorf("filesystem operations require the resident Core: %w", err)
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	client, err := control.NewClient(corepkg.ControlSocketPath(state.Cfg, corepkg.RuntimeLayout{RunDir: runDir}))
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	return ctx, fsclient.New(client), stop, nil
}
