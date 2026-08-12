package debug

import (
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
)

func NewUploadMemoryCmd(rt cliruntime.Runtime) *cobra.Command {
	var history bool
	var since string
	var limit int
	var watch time.Duration
	cmd := &cobra.Command{
		Use:               "upload-memory",
		Short:             "Inspect upload memory diagnostics",
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			mounts, _, err := debugMountScopeFromFlags(rt, cmd)
			if err != nil {
				return err
			}
			endpoint := uploadMemoryEndpoint(mounts, history, since, limit)
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
	cmd.Flags().BoolVar(&history, "history", false, "include upload history")
	cmd.Flags().StringVar(&since, "since", "2m", "driver metric lookback duration or RFC3339 timestamp")
	cmd.Flags().IntVar(&limit, "limit", 20, "recent upload part metric limit")
	cmd.Flags().DurationVar(&watch, "watch", 0, "repeat at this interval")
	return cmd
}

func uploadMemoryEndpoint(mounts []string, history bool, since string, limit int) string {
	q := url.Values{}
	if history {
		q.Set("history", "1")
	}
	if since != "" {
		q.Set("since", since)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	endpoint := "/v1/upload-memory"
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return debugEndpointWithMounts(endpoint, mounts)
}
