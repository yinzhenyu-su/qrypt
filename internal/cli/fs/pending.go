package fs

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func NewPendingCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Show pending uploads",
		Args:  cliruntime.NoArgs(rt),
		RunE:  runPending(rt),
	}
	cmd.Flags().Bool("verbose", false, "show detailed output")
	cmd.Flags().Bool("json", false, "write JSON output")
	return cmd
}

func runPending(rt cliruntime.Runtime) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		_, fs, cleanup, err := rt.OpenFileSystem(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		verbose, _ := cmd.Flags().GetBool("verbose")
		asJSON, _ := cmd.Flags().GetBool("json")
		if verbose && asJSON {
			return fmt.Errorf("--verbose and --json cannot be used together")
		}
		pending := cliruntime.PendingFiles(fs)
		if asJSON {
			if pending == nil {
				pending = []vfs.PendingUpload{}
			}
			return cliruntime.WritePrettyJSON(cmd.OutOrStdout(), pending)
		}
		if verbose {
			PrintPendingVerbose(cmd.OutOrStdout(), pending)
			return nil
		}
		for _, item := range pending {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %d %s\n", item.Path, item.Size, item.LocalPath)
		}
		return nil
	}
}

func PrintPendingVerbose(w io.Writer, pending []vfs.PendingUpload) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tSIZE\tLOCAL\tSTAGING\tRETRY\tLAST_ATTEMPT\tNEXT_ATTEMPT\tLAST_ERROR")
	for _, item := range pending {
		status, size := cliruntime.StagingStatus(item)
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			item.Path,
			item.Size,
			item.LocalPath,
			cliruntime.FormatStagingStatus(status, size),
			item.RetryCount,
			cliruntime.FormatUnixNano(item.LastAttemptAt),
			cliruntime.FormatUnixNano(item.NextAttemptAt),
			item.LastError,
		)
	}
	_ = tw.Flush()
}
