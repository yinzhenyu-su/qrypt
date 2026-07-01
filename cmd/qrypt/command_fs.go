package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
	return &cobra.Command{
		Use:   "list [path]",
		Short: "List a directory",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runList,
	}
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
	for _, entry := range entries {
		kind := "file"
		if entry.IsDir {
			kind = "dir "
		}
		fmt.Printf("%s %10d %s\n", kind, entry.Size, entry.Name)
	}
	return nil
}

func newFsCatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cat REMOTE",
		Short: "Write a remote file to stdout",
		Args:  cobra.ExactArgs(1),
		RunE:  runCat,
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
	_, err = io.Copy(os.Stdout, rc)
	return err
}

func newFsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get REMOTE LOCAL",
		Short: "Download a remote file",
		Args:  cobra.ExactArgs(2),
		RunE:  runGet,
	}
}

func runGet(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	return get(ctx, fs, args[0], args[1])
}

func get(ctx context.Context, fs vfs.FileSystem, remotePath, localPath string) error {
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

	out, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func newFsPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "put LOCAL REMOTE",
		Short: "Upload a local file",
		Args:  cobra.ExactArgs(2),
		RunE:  runPut,
	}
}

func runPut(cmd *cobra.Command, args []string) error {
	ctx, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := put(ctx, fs, args[0], args[1]); err != nil {
		return err
	}
	return waitFileSystemIdle(fs)
}

func newFsPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Show pending uploads",
		Args:  cobra.NoArgs,
		RunE:  runPending,
	}
	cmd.Flags().Bool("verbose", false, "show detailed output")
	return cmd
}

func runPending(cmd *cobra.Command, args []string) error {
	_, fs, cleanup, err := openFileSystem(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		printPendingVerbose(os.Stdout, fs.Pending())
		return nil
	}
	for _, pending := range fs.Pending() {
		fmt.Printf("%s %d %s\n", pending.Path, pending.Size, pending.LocalPath)
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
	return &cobra.Command{
		Use:   "stat PATH",
		Short: "Show path metadata",
		Args:  cobra.ExactArgs(1),
		RunE:  runStat,
	}
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
	printEntryStat(os.Stdout, entry)
	return nil
}

func newFsMkdirCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mkdir PATH",
		Short: "Create a directory",
		Args:  cobra.ExactArgs(1),
		RunE:  runMkdir,
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
	return &cobra.Command{
		Use:   "rm PATH",
		Short: "Remove a file or empty directory",
		Args:  cobra.ExactArgs(1),
		RunE:  runRm,
	}
}

func runRm(cmd *cobra.Command, args []string) error {
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
		return waitFileSystemIdle(fs)
	}
	if err := fs.Remove(ctx, args[0]); err != nil {
		return err
	}
	return waitFileSystemIdle(fs)
}

func newFsMvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mv SRC DST",
		Short: "Rename or move a path",
		Args:  cobra.ExactArgs(2),
		RunE:  runMv,
	}
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
	if err := requireConfig(); err != nil {
		return nil, nil, nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	fs, cleanup, err := buildFileSystem(ctx, cmd, driverName, root, "", configPath, mountName, password, salt, fileNameEncryption, fileNameEncoding)
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

func waitFileSystemIdle(fs vfs.FileSystem) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if len(fs.Pending()) == 0 && debugDeleteTimers(fs) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("filesystem operations still pending")
		}
		time.Sleep(50 * time.Millisecond)
	}
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
