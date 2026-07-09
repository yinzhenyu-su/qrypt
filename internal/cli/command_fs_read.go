package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/fileutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

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
		return writePrettyJSON(cmd.OutOrStdout(), entries)
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
	return fileutil.WriteAtomic(localPath, ".qrypt-download-*", 0o644, force, func(file *os.File) error {
		return copyRemoteFile(ctx, fs, remotePath, file)
	})
}
