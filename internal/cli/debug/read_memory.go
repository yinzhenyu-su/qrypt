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
	cmd := &cobra.Command{
		Use:               "read-memory",
		Short:             "Inspect read-path memory diagnostics",
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
				if _, err := cmd.OutOrStdout().Write(body); err != nil {
					return err
				}
				if _, err := cmd.OutOrStdout().Write([]byte("\n")); err != nil {
					return err
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
