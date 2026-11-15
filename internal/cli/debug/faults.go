package debug

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

// NewFaultCmd owns probe-only fault injection. Rules are armed on the running
// mount, so a test can reproduce provider stalls, truncation, and retryable
// statuses without a real bad CDN edge.
func NewFaultCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fault",
		Short: "Inject probe-only read faults",
		Long: `Arm, list, and clear read faults on a running mount. Faults are handled
by the shared read layer exactly like provider failures, so they exercise the
idle timeout, short-body replay, and retryable-status backoff paths.`,
		Example: `  qrypt probe fault read --path /cloud/movie.mp4 --kind stall --after 1m
  qrypt probe fault list`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newFaultReadCmd(rt)))
	cmd.AddCommand(withProbeSocketFlag(rt, newFaultListCmd(rt)))
	cmd.AddCommand(withProbeSocketFlag(rt, newFaultClearCmd(rt)))
	return cmd
}

func newFaultReadCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Arm one read fault",
		Long: `Arm one read fault for a file. stall stops producing bytes after --after,
truncate ends the body there, status returns an HTTP status, and delay waits
before the body is returned. The rule fires --times times (default once), so
the first failed attempt is followed by a healthy replay.`,
		Example: `  qrypt probe fault read --path /cloud/movie.mp4 --kind stall --after 1m
  qrypt probe fault read --path /cloud/movie.mp4 --kind truncate --after 2m --times 1
  qrypt probe fault read --path /cloud/movie.mp4 --kind status --status 500`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _ := cmd.Flags().GetString("path")
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("fault read requires --path")
			}
			kind, _ := cmd.Flags().GetString("kind")
			kind = strings.ToLower(strings.TrimSpace(kind))
			offsetRaw, _ := cmd.Flags().GetString("offset")
			lengthRaw, _ := cmd.Flags().GetString("length")
			if strings.TrimSpace(lengthRaw) != "" && strings.TrimSpace(offsetRaw) == "" {
				return fmt.Errorf("fault read --length requires --offset")
			}
			afterRaw, _ := cmd.Flags().GetString("after")
			delay, _ := cmd.Flags().GetString("delay")
			status, _ := cmd.Flags().GetInt("status")
			times, _ := cmd.Flags().GetInt("times")
			includePrefetch, _ := cmd.Flags().GetBool("include-prefetch")
			reason, _ := cmd.Flags().GetString("reason")
			request := map[string]any{
				"path": path,
				"kind": kind,
			}
			if includePrefetch {
				request["include_prefetch"] = true
			}
			if strings.TrimSpace(offsetRaw) == "" {
				request["any_offset"] = true
			} else {
				offset := contracttest.ParseXferSize(offsetRaw)
				if offsetRaw != "0" && offset <= 0 {
					return fmt.Errorf("fault read --offset must be a non-negative byte count")
				}
				request["offset"] = offset
			}
			if strings.TrimSpace(lengthRaw) != "" {
				length := contracttest.ParseXferSize(lengthRaw)
				if length <= 0 {
					return fmt.Errorf("fault read --length must be a positive byte count")
				}
				request["length"] = length
			}
			if strings.TrimSpace(afterRaw) != "" {
				after := contracttest.ParseXferSize(afterRaw)
				if afterRaw != "0" && after <= 0 {
					return fmt.Errorf("fault read --after must be a non-negative byte count")
				}
				request["after_bytes"] = after
			}
			if strings.TrimSpace(delay) != "" {
				request["delay"] = delay
			}
			if status > 0 {
				request["status"] = status
			}
			if times > 0 {
				request["times"] = times
			}
			if strings.TrimSpace(reason) != "" {
				request["reason"] = reason
			}
			body, err := debugSocketPostJSON(cmd.Context(), "/v1/debug/faults/read", request)
			if err != nil {
				if detail, routeMissing := debugSocket404Detail(err); detail != "" {
					return fmt.Errorf("fault read failed: %s", detail)
				} else if routeMissing {
					return fmt.Errorf("fault injection endpoint is not available on this socket; restart the qrypt mount process with the current binary")
				}
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
	cmd.Flags().String("path", "", "virtual path of the file to inject into")
	cmd.Flags().String("kind", "", "fault kind: stall, truncate, status, or delay")
	cmd.Flags().String("offset", "", "byte offset the fault applies to (default: any window)")
	cmd.Flags().String("length", "", "fault range length from --offset (default: target one offset)")
	cmd.Flags().String("after", "", "bytes delivered before a stall or truncate fires (default 0)")
	cmd.Flags().String("delay", "", "open delay for --kind delay, e.g. 2s")
	cmd.Flags().Int("status", 0, "HTTP status for --kind status (default 500)")
	cmd.Flags().Int("times", 1, "number of times the rule fires")
	cmd.Flags().Bool("include-prefetch", false, "let the rule fire on background prefetch reads")
	cmd.Flags().String("reason", "", "diagnostic reason stored with the rule")
	return cmd
}

func newFaultListCmd(rt cliruntime.Runtime) *cobra.Command {
	return &cobra.Command{
		Use:               "list",
		Short:             "List armed read faults",
		Long:              `List every read fault on the running mount, including rules whose remaining count reached zero.`,
		Example:           `  qrypt probe fault list`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := debugSocketGet(cmd.Context(), "/v1/debug/faults/read")
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(body)
			return err
		},
	}
}

func newFaultClearCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear read faults",
		Long:  `Clear one read fault by id, or every read fault when --id is omitted.`,
		Example: `  qrypt probe fault clear --id read-fault-1
  qrypt probe fault clear`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			endpoint := "/v1/debug/faults/read"
			if strings.TrimSpace(id) != "" {
				endpoint += "?id=" + url.QueryEscape(strings.TrimSpace(id))
			}
			_, err := debugSocketDelete(cmd.Context(), endpoint)
			return err
		},
	}
	cmd.Flags().String("id", "", "fault id to clear (default: all faults)")
	return cmd
}
