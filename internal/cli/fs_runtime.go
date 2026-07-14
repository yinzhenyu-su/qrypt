package cli

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func openFileSystem(cmd *cobra.Command) (context.Context, vfs.FileSystem, func(), error) {
	state, err := commandConfig(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	if state.cfg == nil {
		return nil, nil, nil, configNotFoundError()
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Config: %s\n", state.path)
	ctx, stop := signal.NotifyContext(commandContext(cmd), shutdownSignals()...)
	selectedMount := commandFSMount(cmd)
	var fs vfs.FileSystem
	var cleanup func()
	if selectedMount != "" {
		fs, cleanup, err = buildFileSystemFromConfigMountNamespace(ctx, state.cfg, selectedMount)
	} else {
		fs, cleanup, err = buildFileSystemFromConfig(ctx, state.cfg)
	}
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	fs.Start(ctx)
	return ctx, fs, func() {
		cleanup()
		stop()
	}, nil
}

func commandFSMount(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	if flag := cmd.Flags().Lookup("mount"); flag != nil {
		value, _ := cmd.Flags().GetString("mount")
		return value
	}
	if flag := cmd.InheritedFlags().Lookup("mount"); flag != nil {
		value, _ := cmd.InheritedFlags().GetString("mount")
		return value
	}
	return ""
}

func printEntryStat(w io.Writer, entry drive.Entry) {
	kind := "file"
	if entry.IsDir {
		kind = "dir"
	}
	fmt.Fprintf(w, "type: %s\n", kind)
	fmt.Fprintf(w, "name: %s\n", entry.Name)
	fmt.Fprintf(w, "id: %s\n", entry.ID)
	fmt.Fprintf(w, "parent_id: %s\n", entry.ParentID)
	fmt.Fprintf(w, "size: %d\n", entry.Size)
	if !entry.ModTime.IsZero() {
		fmt.Fprintf(w, "mod_time: %s\n", entry.ModTime.Format(time.RFC3339))
	}
}

func waitFileSystemIdle(ctx context.Context, fs vfs.FileSystem, timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("--wait-timeout must be greater than 0")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		uploads, deleteTimers := fileSystemActivity(fs)
		if len(fs.Pending()) == 0 && uploads == 0 && deleteTimers == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("filesystem operations still pending after %s", timeout)
		case <-ticker.C:
		}
	}
}

func commandWaitTimeout(cmd *cobra.Command) time.Duration {
	if cmd.Flag("wait-timeout") == nil {
		return 30 * time.Second
	}
	timeout, _ := cmd.Flags().GetDuration("wait-timeout")
	return timeout
}

func fileSystemActivity(fs vfs.FileSystem) (uploads, deleteTimers int) {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0, 0
	}
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		uploads += len(mount.ActiveUploads())
		deleteTimers += len(mount.ActiveDeleteTimers())
	}
	return uploads, deleteTimers
}
