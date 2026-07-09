package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

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
