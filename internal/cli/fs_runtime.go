package cli

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
)

// builtFS is a filesystem the CLI constructed and will start. Every builder
// path returns a value with the full file-operation surface plus the
// lifecycle hook, so commands can Start it without type assertions.
type builtFS interface {
	vfs.FileSystem
	vfs.Lifecycle
}

func openFileSystem(cmd *cobra.Command) (context.Context, builtFS, func(), error) {
	state, err := commandConfig(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	if state.cfg == nil {
		return nil, nil, nil, configNotFoundError()
	}
	ctx, stop := signal.NotifyContext(commandContext(cmd), shutdownSignals()...)
	selectedMount := commandFSMount(cmd)
	bandwidth, err := bandwidthOverrideFromFlags(cmd)
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	var fs builtFS
	var cleanup func()
	if selectedMount != "" {
		fs, cleanup, err = buildFileSystemWithBandwidth(ctx, state.cfg, selectedMount, true, bandwidth)
	} else {
		fs, cleanup, err = buildFileSystemWithBandwidth(ctx, state.cfg, "", true, bandwidth)
	}
	if err != nil {
		stop()
		return nil, nil, nil, err
	}
	fs.Start(ctx)
	return ctx, fs, func() {
		// Shut down in dependency order: wait for the filesystem's workers
		// (journal/staging writes, read-cache flush) to finish BEFORE
		// cleaning up external resources, then stop the signal context.
		// Cancelling first would make Close asynchronous and let cleanup
		// race the still-running workers.
		if err := fs.Close(context.Background()); err != nil {
			logging.L.Warnf("[CLI] filesystem close: %v", err)
		}
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

func waitFileSystemIdle(ctx context.Context, fs any, timeout time.Duration) error {
	// No deadline (timeout <= 0): wait until the filesystem is idle or the
	// context is cancelled (Ctrl-C). fs sync uses this because its transfer
	// size is unbounded; a fixed timeout would report a healthy slow upload
	// as a failure. A non-positive timer never fires, so stopping it keeps
	// the select below simple.
	timer := time.NewTimer(timeout)
	if timeout <= 0 {
		timer.Stop()
	}
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		uploads, deleteTimers := fileSystemActivity(fs)
		pending, err := pendingFiles(fs)
		if err != nil {
			return err
		}
		if len(pending) == 0 && uploads == 0 && deleteTimers == 0 {
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

func fileSystemActivity(fs any) (uploads, deleteTimers int) {
	snapshotter, ok := fs.(diagnostics.DebugSnapshotProvider)
	if !ok {
		return 0, 0
	}
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		uploads += len(mount.ActiveUploads())
		deleteTimers += len(mount.ActiveDeleteTimers())
	}
	return uploads, deleteTimers
}

// bandwidthOverrideFromFlags maps the fs --bwlimit flags onto bandwidth
// limits. nil means no override (use the config [bandwidth] section).
func bandwidthOverrideFromFlags(cmd *cobra.Command) (*config.BandwidthLimits, error) {
	both := flagStringValue(cmd, "bwlimit")
	download := flagStringValue(cmd, "bwlimit-download")
	upload := flagStringValue(cmd, "bwlimit-upload")
	if both == "" && download == "" && upload == "" {
		return nil, nil
	}
	parse := func(flagName, value string) (int64, error) {
		if value == "" {
			return 0, nil
		}
		n, err := config.ParseSize(value)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", flagName, err)
		}
		return n, nil
	}
	limits := &config.BandwidthLimits{}
	if both != "" {
		n, err := parse("--bwlimit", both)
		if err != nil {
			return nil, err
		}
		limits.DownloadBytesPerSecond = n
		limits.UploadBytesPerSecond = n
	}
	if download != "" {
		down, err := parse("--bwlimit-download", download)
		if err != nil {
			return nil, err
		}
		limits.DownloadBytesPerSecond = down
	}
	if upload != "" {
		up, err := parse("--bwlimit-upload", upload)
		if err != nil {
			return nil, err
		}
		limits.UploadBytesPerSecond = up
	}
	if limits.DownloadBytesPerSecond <= 0 && limits.UploadBytesPerSecond <= 0 {
		return nil, fmt.Errorf("bandwidth limit must be greater than zero")
	}
	return limits, nil
}

func addFSBandwidthFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("bwlimit", "", "limit both download and upload bandwidth (e.g. 10M)")
	cmd.PersistentFlags().String("bwlimit-download", "", "limit download bandwidth (e.g. 10M)")
	cmd.PersistentFlags().String("bwlimit-upload", "", "limit upload bandwidth (e.g. 10M)")
}

// flagStringValue reads a string flag that may live on the command or any
// of its parents (persistent flags).
func flagStringValue(cmd *cobra.Command, name string) string {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	return ""
}
