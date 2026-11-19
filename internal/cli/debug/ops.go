package debug

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// NewOpsCmd lists in-flight mount operations: foreground windows, speculative
// prefetches, uploads, and other active work reported by the read layer.
func NewOpsCmd(rt cliruntime.Runtime) *cobra.Command {
	var mounts []string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "List in-flight mount operations",
		Long: `List the operations currently in flight inside the running mount,
including foreground read windows, speculative prefetches, and uploads. Use
--mount to narrow the output to one mount.`,
		Example: `  qrypt debug ops --mount cloud
  qrypt debug ops --json`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := debugSocketGet(cmd.Context(), opsEndpoint(mounts))
			if err != nil {
				return err
			}
			if jsonOutput {
				envelope := newDiagnosticsEnvelope("debug ops", debugSocketFromContext(cmd.Context()), body)
				return writeDiagnosticsEnvelope(cmd.OutOrStdout(), envelope, true)
			}
			var response control.ActiveOpsResponse
			if err := json.Unmarshal(body, &response); err != nil {
				_, writeErr := cmd.OutOrStdout().Write(body)
				return writeErr
			}
			return printDebugOps(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().StringArrayVar(&mounts, "mount", nil, "mount name to inspect (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print stable JSON instead of human-readable lines")
	return cmd
}

func opsEndpoint(mounts []string) string {
	return debugEndpointWithMounts("/v1/ops", cleanDebugMountNames(mounts))
}

func printDebugOps(out io.Writer, response control.ActiveOpsResponse) error {
	rows := 0
	for _, mount := range response.Mounts {
		for _, op := range mount.Ops {
			rows++
			phase := op.Phase
			if phase == "" {
				phase = "-"
			}
			intent := op.ReadIntent
			if intent == "" {
				intent = "-"
			}
			path := op.Path
			if path == "" {
				path = "-"
			}
			if _, err := fmt.Fprintf(out, "%s %-14s %-16s %-10s %-9s %s %s\n",
				mount.Mount, op.Kind, phase, op.State, intent, path, activeOpDetail(op)); err != nil {
				return err
			}
		}
	}
	if rows == 0 {
		_, err := fmt.Fprintln(out, "no active operations")
		return err
	}
	return nil
}

func activeOpDetail(op vfstypes.DebugActiveOp) string {
	var parts []string
	if op.OpID != "" {
		parts = append(parts, "op="+op.OpID)
	}
	if op.RemoteID != "" {
		parts = append(parts, "id="+op.RemoteID)
	}
	if op.Requested > 0 {
		parts = append(parts, fmt.Sprintf("requested=%d", op.Requested))
	}
	if op.Offset > 0 {
		parts = append(parts, fmt.Sprintf("offset=%d", op.Offset))
	}
	if op.Background {
		parts = append(parts, "background=true")
	}
	if op.WaitFor != "" {
		parts = append(parts, "wait_for="+op.WaitFor)
	}
	if op.AgeMS > 0 {
		parts = append(parts, fmt.Sprintf("age_ms=%d", op.AgeMS))
	}
	return strings.Join(parts, " ")
}
