package fs

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

func NewPutCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "put LOCAL REMOTE",
		Short: "Upload a local file; use - to read from stdin",
		Args:  cliruntime.ExactNamedArgs(rt, "LOCAL", "REMOTE"),
		RunE:  runPut(rt),
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

func runPut(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
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

		if args[0] == "-" {
			err = putReader(ctx, fs, cmd.InOrStdin(), args[1])
		} else {
			err = put(ctx, fs, util.ExpandHome(args[0]), args[1])
		}
		if err != nil {
			return err
		}
		return rt.WaitFileSystemIdle(ctx, fs, waitTimeout)
	}
}
