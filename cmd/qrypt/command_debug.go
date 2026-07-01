package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

var debugSocket string

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Inspect live runtime and cache state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&debugSocket, "debug-socket", "", "debug socket path")
	cmd.AddCommand(newDebugBundleCmd())
	cmd.AddCommand(debugHealth())
	cmd.AddCommand(debugState())
	cmd.AddCommand(debugRuntime())
	cmd.AddCommand(debugDriver())
	cmd.AddCommand(debugList())
	cmd.AddCommand(debugEvents())
	cmd.AddCommand(debugUploads())
	cmd.AddCommand(debugResolve())
	cmd.AddCommand(debugCache())
	cmd.AddCommand(debugStaging())
	cmd.AddCommand(debugConsistency())
	cmd.AddCommand(debugGoroutines())
	cmd.AddCommand(debugDoctor())
	return cmd
}

func newDebugBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Write a debug bundle zip",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			outPath, _ := cmd.Flags().GetString("out")
			if debugSocket == "" {
				return fmt.Errorf("this command requires --debug-socket")
			}
			if outPath == "" {
				return fmt.Errorf("usage: qrypt debug bundle --out FILE (requires --debug-socket)")
			}
			client, err := control.NewClient(debugSocket)
			if err != nil {
				return err
			}
			out, err := os.Create(outPath)
			if err != nil {
				return err
			}
			defer out.Close()
			zw := zip.NewWriter(out)
			endpoints := map[string]string{
				"health.json":    "/v1/health",
				"state.json":     "/v1/state",
				"pending.json":   "/v1/pending",
				"uploads.json":   "/v1/uploads?history=1",
				"events.json":    "/v1/events?level=warn&limit=500",
				"cache.json":     "/v1/cache",
				"staging.json":   "/v1/staging",
				"driver.json":    "/v1/driver",
				"runtime.json":   "/v1/runtime",
				"goroutines.txt": "/v1/goroutines?debug=1",
			}
			names := make([]string, 0, len(endpoints))
			for name := range endpoints {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				body, err := client.Get(ctx, endpoints[name])
				if err != nil {
					body = []byte(err.Error() + "\n")
				}
				w, err := zw.Create(name)
				if err != nil {
					return err
				}
				if _, err := w.Write(body); err != nil {
					return err
				}
			}
			return zw.Close()
		},
	}
	cmd.Flags().String("out", "", "debug bundle output zip")
	return cmd
}

func parsePendingArgs(args []string) (bool, error) {
	verbose := false
	for _, arg := range args {
		switch arg {
		case "-v", "--verbose":
			verbose = true
		default:
			return false, fmt.Errorf("usage: qrypt [flags] pending [-v|--verbose]")
		}
	}
	return verbose, nil
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
