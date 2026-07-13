package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func newFsPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Show pending uploads",
		Args:  noArgs,
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
		return writePrettyJSON(cmd.OutOrStdout(), pending)
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
