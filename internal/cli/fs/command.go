package fs

import (
	"github.com/spf13/cobra"
	clijournal "github.com/yinzhenyu/qrypt/internal/cli/journal"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func NewCommand(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Run one-shot filesystem operations",
		Args:  cliruntime.CommandGroupArgs(rt, nil),
		RunE:  cliruntime.ShowHelp,
	}
	rt.WithPersistentRuntimeConfigFlag(cmd)
	rt.WithFSBandwidthFlags(cmd)
	cmd.PersistentFlags().String("mount", "", "only initialize one mount while keeping namespace paths such as /MOUNT/path")
	cmd.AddCommand(NewListCmd(rt))
	cmd.AddCommand(NewCatCmd(rt))
	cmd.AddCommand(NewGetCmd(rt))
	cmd.AddCommand(NewPutCmd(rt))
	cmd.AddCommand(NewPendingCmd(rt))
	cmd.AddCommand(NewStatCmd(rt))
	cmd.AddCommand(NewMkdirCmd(rt))
	cmd.AddCommand(NewRmCmd(rt))
	cmd.AddCommand(NewMvCmd(rt))
	cmd.AddCommand(NewCopyCmd(rt))
	cmd.AddCommand(NewDfCmd(rt))
	cmd.AddCommand(NewDuCmd(rt))
	cmd.AddCommand(NewFindCmd(rt))
	cmd.AddCommand(NewCheckCmd(rt))
	cmd.AddCommand(NewSyncCmd(rt))
	cmd.AddCommand(NewCryptEncodeCmd(rt))
	cmd.AddCommand(NewCryptDecodeCmd(rt))
	cmd.AddCommand(clijournal.NewCommand(rt, "journal"))
	return cmd
}
