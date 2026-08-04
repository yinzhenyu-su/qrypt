package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newFsStatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stat REMOTE",
		Short:             "Show path metadata",
		Args:              exactNamedArgs("REMOTE"),
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
		return writePrettyJSON(cmd.OutOrStdout(), entry)
	}
	printEntryStat(cmd.OutOrStdout(), entry)
	return nil
}

func newFsMkdirCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "mkdir REMOTE",
		Short:             "Create a directory",
		Args:              exactNamedArgs("REMOTE"),
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
		Args:              exactNamedArgs("REMOTE"),
		RunE:              runRm,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Duration("wait-timeout", 30*time.Second, "maximum time to wait for deletion to finish")
	cmd.Flags().Bool("json", false, "write JSON output")
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
		if err := waitFileSystemIdle(ctx, fs, waitTimeout); err != nil {
			return err
		}
	} else {
		if err := fs.Remove(ctx, args[0]); err != nil {
			return err
		}
		if err := waitFileSystemIdle(ctx, fs, waitTimeout); err != nil {
			return err
		}
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), struct {
			Path    string `json:"path"`
			Removed bool   `json:"removed"`
		}{Path: args[0], Removed: true})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
	return nil
}

func newFsMvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "mv SOURCE DESTINATION",
		Short:             "Rename or move a path",
		Args:              exactNamedArgs("SOURCE", "DESTINATION"),
		RunE:              runMv,
		ValidArgsFunction: noFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
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
	if err := fs.Rename(ctx, args[0], args[1]); err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return writePrettyJSON(cmd.OutOrStdout(), struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
			Renamed     bool   `json:"renamed"`
		}{Source: args[0], Destination: args[1], Renamed: true})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "renamed %s -> %s\n", args[0], args[1])
	return nil
}
