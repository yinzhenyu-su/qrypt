package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Run one-shot filesystem operations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	withPersistentRuntimeConfigFlag(cmd)
	cmd.AddCommand(newFsListCmd())
	cmd.AddCommand(newFsCatCmd())
	cmd.AddCommand(newFsGetCmd())
	cmd.AddCommand(newFsPutCmd())
	cmd.AddCommand(newFsPendingCmd())
	cmd.AddCommand(newFsStatCmd())
	cmd.AddCommand(newFsMkdirCmd())
	cmd.AddCommand(newFsRmCmd())
	cmd.AddCommand(newFsMvCmd())
	return cmd
}

func newFsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list [REMOTE]",
		Short:             "List a directory",
		Args:              cobra.MaximumNArgs(1),
		RunE:              runList,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	path := "/"
	if len(args) > 0 {
		path = args[0]
	}
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	entries, err := fs.List(ctx, path)
	if err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if entries == nil {
			entries = []drive.Entry{}
		}
		return writeJSON(cmd.OutOrStdout(), entries)
	}
	for _, entry := range entries {
		kind := "file"
		if entry.IsDir {
			kind = "dir "
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %10d %s\n", kind, entry.Size, entry.Name)
	}
	return nil
}

func newFsCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "cat REMOTE",
		Short:             "Write a remote file to stdout",
		Args:              cobra.ExactArgs(1),
		RunE:              runCat,
		ValidArgsFunction: noFileCompletions,
	}
}

func runCat(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	rc, err := fs.Read(ctx, args[0], 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(cmd.OutOrStdout(), rc)
	return err
}

func newFsGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get REMOTE LOCAL",
		Short: "Download a remote file; use - to write to stdout",
		Args:  cobra.ExactArgs(2),
		RunE:  runGet,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveDefault
		},
	}
	cmd.Flags().BoolP("force", "f", false, "overwrite an existing local file")
	return cmd
}

func runGet(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if args[1] == "-" {
		return copyRemoteFile(ctx, fs, args[0], cmd.OutOrStdout())
	}
	force, _ := cmd.Flags().GetBool("force")
	return get(ctx, fs, args[0], osutil.ExpandHome(args[1]), force)
}

func copyRemoteFile(ctx context.Context, fs vfs.FileSystem, remotePath string, out io.Writer) error {
	entry, err := fs.Stat(ctx, remotePath)
	if err != nil {
		return err
	}
	if entry.IsDir {
		return fmt.Errorf("%s is a directory", remotePath)
	}

	rc, err := fs.Read(ctx, remotePath, 0, 0)
	if err != nil {
		return err
	}
	defer rc.Close()

	_, err = io.Copy(out, rc)
	return err
}

func get(ctx context.Context, fs vfs.FileSystem, remotePath, localPath string, force bool) error {
	if info, err := os.Stat(localPath); err == nil {
		if info.IsDir() {
			return fmt.Errorf("local destination %q is a directory", localPath)
		}
		if !force {
			return fmt.Errorf("local destination %q already exists (use --force to overwrite)", localPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".qrypt-download-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copyRemoteFile(ctx, fs, remotePath, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if force {
		return replaceLocalFile(tmpPath, localPath)
	}
	return os.Rename(tmpPath, localPath)
}

func newFsPutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "put LOCAL REMOTE",
		Short: "Upload a local file; use - to read from stdin",
		Args:  cobra.ExactArgs(2),
		RunE:  runPut,
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return nil, cobra.ShellCompDirectiveDefault
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
	}
	cmd.Flags().Duration("wait-timeout", 30*time.Second, "maximum time to wait for the upload to finish")
	return cmd
}

func runPut(cmd *cobra.Command, args []string) error {
	waitTimeout := commandWaitTimeout(cmd)
	if waitTimeout <= 0 {
		return fmt.Errorf("--wait-timeout must be greater than 0")
	}
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if args[0] == "-" {
		err = putReader(ctx, fs, cmd.InOrStdin(), args[1])
	} else {
		err = put(ctx, fs, osutil.ExpandHome(args[0]), args[1])
	}
	if err != nil {
		return err
	}
	return waitFileSystemIdle(ctx, fs, waitTimeout)
}

func newFsPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Show pending uploads",
		Args:  cobra.NoArgs,
		RunE:  runPending,
	}
	cmd.Flags().Bool("verbose", false, "show detailed output")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runPending(cmd *cobra.Command, args []string) error {
	_, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	verbose, _ := cmd.Flags().GetBool("verbose")
	asJSON, _ := cmd.Flags().GetBool("json")
	if verbose && asJSON {
		return fmt.Errorf("--verbose and --json cannot be used together")
	}
	if asJSON {
		pending := fs.Pending()
		if pending == nil {
			pending = []vfs.PendingFile{}
		}
		return writeJSON(cmd.OutOrStdout(), pending)
	}
	if verbose {
		printPendingVerbose(cmd.OutOrStdout(), fs.Pending())
		return nil
	}
	for _, pending := range fs.Pending() {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %d %s\n", pending.Path, pending.Size, pending.LocalPath)
	}
	return nil
}

func printPendingVerbose(w io.Writer, pending []vfs.PendingFile) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSIZE\tLOCAL\tSTAGING\tRETRY\tLAST_ATTEMPT\tNEXT_ATTEMPT\tLAST_ERROR")
	for _, item := range pending {
		status, size := stagingStatus(item)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			item.Path,
			item.Size,
			item.LocalPath,
			formatStagingStatus(status, size),
			item.RetryCount,
			formatUnixNano(item.LastAttemptAt),
			formatUnixNano(item.NextAttemptAt),
			item.LastError,
		)
	}
	_ = tw.Flush()
}

func newFsStatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stat REMOTE",
		Short:             "Show path metadata",
		Args:              cobra.ExactArgs(1),
		RunE:              runStat,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runStat(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	entry, err := fs.Stat(ctx, args[0])
	if err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), entry)
	}
	printEntryStat(cmd.OutOrStdout(), entry)
	return nil
}

func newFsMkdirCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mkdir REMOTE",
		Short:             "Create a directory",
		Args:              cobra.ExactArgs(1),
		RunE:              runMkdir,
		ValidArgsFunction: noFileCompletions,
	}
}

func runMkdir(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	_, err = fs.Mkdir(ctx, args[0])
	return err
}

func newFsRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "rm REMOTE",
		Short:             "Remove a file or empty directory",
		Args:              cobra.ExactArgs(1),
		RunE:              runRm,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Duration("wait-timeout", 30*time.Second, "maximum time to wait for deletion to finish")
	return cmd
}

func runRm(cmd *cobra.Command, args []string) error {
	waitTimeout := commandWaitTimeout(cmd)
	if waitTimeout <= 0 {
		return fmt.Errorf("--wait-timeout must be greater than 0")
	}
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	entry, err := fs.Stat(ctx, args[0])
	if err != nil {
		return err
	}
	if entry.IsDir {
		if err := fs.RemoveDir(ctx, args[0]); err != nil {
			return err
		}
		return waitFileSystemIdle(ctx, fs, waitTimeout)
	}
	if err := fs.Remove(ctx, args[0]); err != nil {
		return err
	}
	return waitFileSystemIdle(ctx, fs, waitTimeout)
}

func newFsMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mv SOURCE DESTINATION",
		Short:             "Rename or move a path",
		Args:              cobra.ExactArgs(2),
		RunE:              runMv,
		ValidArgsFunction: noFileCompletions,
	}
}

func noFileCompletions(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func runMv(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	return fs.Rename(ctx, args[0], args[1])
}

func openFileSystem(cmd *cobra.Command) (context.Context, vfs.FileSystem, func(), error) {
	configPath, err := commandConfigPath(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	if configPath == "" {
		return nil, nil, nil, configNotFoundError()
	}
	if err := requireConfig(configPath); err != nil {
		return nil, nil, nil, err
	}
	ctx, stop := signal.NotifyContext(commandContext(cmd), shutdownSignals()...)
	fs, cleanup, err := buildFileSystem(ctx, configPath)
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
		if len(fs.Pending()) == 0 && debugActiveUploads(fs) == 0 && debugDeleteTimers(fs) == 0 {
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

func debugActiveUploads(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.Uploads)
	}
	return count
}

func debugDeleteTimers(fs vfs.FileSystem) int {
	snapshotter, ok := fs.(interface {
		DebugSnapshot() vfs.DebugSnapshot
	})
	if !ok {
		return 0
	}
	count := 0
	for _, mount := range snapshotter.DebugSnapshot().Mounts {
		count += len(mount.DeleteTimers)
	}
	return count
}
