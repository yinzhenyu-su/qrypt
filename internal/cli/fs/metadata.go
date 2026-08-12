package fs

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func NewStatCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stat REMOTE",
		Short:             "Show path metadata",
		Args:              cliruntime.ExactNamedArgs(rt, "REMOTE"),
		RunE:              runStat(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runStat(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
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
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), entry)
		}
		cliruntime.PrintEntryStat(cmd.OutOrStdout(), entry)
		return nil
	}
}

func NewMkdirCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:               "mkdir REMOTE",
		Short:             "Create a directory",
		Args:              cliruntime.ExactNamedArgs(rt, "REMOTE"),
		RunE:              runMkdir(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
}

func runMkdir(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		_, err = fs.Mkdir(ctx, args[0])
		return err
	}
}

func NewRmCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "rm REMOTE",
		Short:             "Remove a file or empty directory",
		Args:              cliruntime.ExactNamedArgs(rt, "REMOTE"),
		RunE:              runRm(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Duration("wait-timeout", 30*time.Second, "maximum time to wait for deletion to finish")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runRm(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		waitTimeout := cliruntime.CommandWaitTimeout(cmd)
		if waitTimeout <= 0 {
			return fmt.Errorf("--wait-timeout must be greater than 0")
		}
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
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
			if err := rt.WaitFileSystemIdle(ctx, fs, waitTimeout); err != nil {
				return err
			}
		} else {
			if err := fs.Remove(ctx, args[0]); err != nil {
				return err
			}
			if err := rt.WaitFileSystemIdle(ctx, fs, waitTimeout); err != nil {
				return err
			}
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
				Path    string `json:"path"`
				Removed bool   `json:"removed"`
			}{Path: args[0], Removed: true})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
		return nil
	}
}

func NewMvCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "mv SOURCE DESTINATION",
		Short:             "Rename or move a path",
		Args:              cliruntime.ExactNamedArgs(rt, "SOURCE", "DESTINATION"),
		RunE:              runMv(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
	}
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runMv(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := fs.Rename(ctx, args[0], args[1]); err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
				Renamed     bool   `json:"renamed"`
			}{Source: args[0], Destination: args[1], Renamed: true})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "renamed %s -> %s\n", args[0], args[1])
		return nil
	}
}
