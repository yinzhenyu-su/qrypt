package cli

import (
	"github.com/spf13/cobra"
)

func newFsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Run one-shot filesystem operations",
		Args:  commandGroupArgs(nil),
		RunE:  showHelp,
	}
	withPersistentRuntimeConfigFlag(cmd)
	addFSBandwidthFlags(cmd)
	cmd.PersistentFlags().String("mount", "", "only initialize one mount while keeping namespace paths such as /MOUNT/path")
	cmd.AddCommand(newFsListCmd())
	cmd.AddCommand(newFsCatCmd())
	cmd.AddCommand(newFsGetCmd())
	cmd.AddCommand(newFsPutCmd())
	cmd.AddCommand(newFsPendingCmd())
	cmd.AddCommand(newFsStatCmd())
	cmd.AddCommand(newFsMkdirCmd())
	cmd.AddCommand(newFsRmCmd())
	cmd.AddCommand(newFsMvCmd())
	cmd.AddCommand(newFsCopyCmd())
	cmd.AddCommand(newFsDfCmd())
	cmd.AddCommand(newFsDuCmd())
	cmd.AddCommand(newFsFindCmd())
	cmd.AddCommand(newFsCheckCmd())
	cmd.AddCommand(newFsCryptEncodeCmd())
	cmd.AddCommand(newFsCryptDecodeCmd())
	cmd.AddCommand(newJournalCmdWithUse("journal"))
	return cmd
}
