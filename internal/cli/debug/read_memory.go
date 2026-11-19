package debug

import (
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func NewReadMemoryCmd(rt cliruntime.Runtime) *cobra.Command {
	var path string
	var since string
	var limit int
	var watch time.Duration
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:               "read-memory",
		Short:             "Inspect read-path memory diagnostics",
		Long:              `Report recent read-path metrics, such as in-flight and cached reads, with an optional path filter, lookback window, and repeat interval.`,
		Example:           `  qrypt debug read-memory --path /cloud/photos --since 5m`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			mounts, _, err := debugMountScopeFromFlags(rt, cmd)
			if err != nil {
				return err
			}
			endpoint := readMemoryEndpoint(mounts, path, since, limit)
			if watch <= 0 {
				body, err := debugSocketGet(cmd.Context(), endpoint)
				if err != nil {
					return err
				}
				if jsonOutput {
					envelope := newDiagnosticsEnvelope("debug read-memory", debugSocketFromContext(cmd.Context()), body)
					return writeDiagnosticsEnvelope(cmd.OutOrStdout(), envelope, true)
				}
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			ticker := time.NewTicker(watch)
			defer ticker.Stop()
			for {
				body, err := debugSocketGet(cmd.Context(), endpoint)
				if err != nil {
					return err
				}
				if jsonOutput {
					// Watch mode emits one compact envelope per poll so a
					// consumer can stream the output line by line.
					envelope := newDiagnosticsEnvelope("debug read-memory", debugSocketFromContext(cmd.Context()), body)
					if err := writeDiagnosticsEnvelope(cmd.OutOrStdout(), envelope, false); err != nil {
						return err
					}
				} else {
					if _, err := cmd.OutOrStdout().Write(body); err != nil {
						return err
					}
					if _, err := cmd.OutOrStdout().Write([]byte("\n")); err != nil {
						return err
					}
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-ticker.C:
				}
			}
		},
	}
	addDebugMountScopeFlags(cmd)
	cmd.Flags().StringVar(&path, "path", "", "virtual path to filter recent reads")
	cmd.Flags().StringVar(&since, "since", "2m", "read metric lookback duration or RFC3339 timestamp")
	cmd.Flags().IntVar(&limit, "limit", 30, "recent read metric limit")
	cmd.Flags().DurationVar(&watch, "watch", 0, "repeat at this interval")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of the raw response body")
	return cmd
}

func readMemoryEndpoint(mounts []string, path string, since string, limit int) string {
	q := url.Values{}
	if path != "" {
		q.Set("path", path)
	}
	if since != "" {
		q.Set("since", since)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	endpoint := "/v1/read-memory"
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return debugEndpointWithMounts(endpoint, mounts)
}
