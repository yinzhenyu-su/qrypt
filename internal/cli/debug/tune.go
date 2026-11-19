package debug

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

// NewTuneCmd groups commands that measure read shapes and recommend a policy.
func NewTuneCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tune",
		Short: "Measure read shapes and recommend a policy",
		Long: `Measure a mounted file under a sequence of read shapes and report the
fastest one. Tuning reads an existing file, so a read-only mount works.`,
		Example: `  qrypt probe tune read --mount cloud --mount-point /mnt/qrypt/cloud --path /videos/sample.mp4
  qrypt probe tune read --mount cloud --mount-point /mnt/qrypt/cloud --path /videos/sample.mp4 --size 64m --rounds 4`,
		Args: cliruntime.CommandGroupArgs(rt, nil),
		RunE: cliruntime.ShowHelp,
	}
	cmd.AddCommand(withProbeSocketFlag(rt, newTuneReadCmd(rt)))
	return cmd
}

func newTuneReadCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Sweep interactive and bulk read shapes",
		Long: `Sweep the interactive window and lookahead depth, then the bulk window and
stream count, against one existing mounted file. Each variant is measured
with a cold playback-shaped read of --size bytes, and the fastest shape is
printed as a TOML block.`,
		Example:           `  qrypt probe tune read --mount cloud --mount-point /mnt/qrypt/cloud --path /videos/sample.mp4 --size 32m --rounds 3`,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTuneRead(cmd)
		},
	}
	cmd.Flags().String("mount", "", "mount name")
	cmd.Flags().String("mount-point", "", "actual FUSE mount directory")
	cmd.Flags().String("path", "", "existing file to tune (virtual path, required)")
	cmd.Flags().String("size", "32m", "bytes read per sample, or k/m/g suffix")
	cmd.Flags().Int("rounds", 3, "measurement rounds per variant")
	cmd.Flags().String("block-size", "16k", "read block size, or k/m/g suffix")
	cmd.Flags().Bool("json", false, "print the raw JSON result")
	return cmd
}

func runTuneRead(cmd *cobra.Command) error {
	req := contracttest.DriverTestRequest{Test: "tuneread"}
	req.Mount, _ = cmd.Flags().GetString("mount")
	req.Mount = contracttest.NormalizeMountName(req.Mount)
	req.MountPoint, _ = cmd.Flags().GetString("mount-point")
	req.Path, _ = cmd.Flags().GetString("path")
	req.Size, _ = cmd.Flags().GetString("size")
	req.Samples, _ = cmd.Flags().GetInt("rounds")
	req.BlockSize, _ = cmd.Flags().GetString("block-size")
	if req.Mount == "" {
		return fmt.Errorf("tune read requires --mount")
	}
	if strings.TrimSpace(req.MountPoint) == "" {
		return fmt.Errorf("tune read requires --mount-point")
	}
	if strings.TrimSpace(req.Path) == "" {
		return fmt.Errorf("tune read requires --path pointing at an existing file")
	}
	body, err := debugSocketPostJSON(cmd.Context(), "/v1/driver/test", req)
	if err != nil {
		if detail, routeMissing := debugSocket404Detail(err); detail != "" {
			return fmt.Errorf("probe tune read failed: %s", detail)
		} else if routeMissing {
			return fmt.Errorf("probe tune endpoint is not available on this socket; restart the qrypt mount process with the current binary")
		}
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		_, err = cmd.OutOrStdout().Write(body)
		return err
	}
	var runs []contracttest.TestRun
	if err := json.Unmarshal(body, &runs); err != nil || len(runs) == 0 {
		var single contracttest.TestRun
		if err := json.Unmarshal(body, &single); err != nil {
			_, werr := cmd.OutOrStdout().Write(body)
			if werr != nil {
				return werr
			}
			return fmt.Errorf("decode tune result: %w", err)
		}
		runs = []contracttest.TestRun{single}
	}
	run := runs[0]
	if run.Tune == nil {
		_, werr := cmd.OutOrStdout().Write(body)
		if werr != nil {
			return werr
		}
		return fmt.Errorf("probe tune read returned no tune payload")
	}
	if run.Error != "" {
		return fmt.Errorf("probe tune read failed: %s", run.Error)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "tuned %s (%s, %d rounds of %s)\n\n", run.Tune.Path, formatTuneSize(run.Tune.SizeBytes), run.Tune.Rounds, formatTuneSize(run.Tune.SizeBytes))
	fmt.Fprintln(out, "  variant                              median      min      max")
	for _, summary := range run.Tune.Summaries {
		fmt.Fprintf(out, "  %-34s %8s %8s %8s\n", summary.Name, formatTuneRate(summary.MedianBPS), formatTuneRate(summary.MinBPS), formatTuneRate(summary.MaxBPS))
	}
	fmt.Fprintln(out)
	if len(run.Tune.Recommendation.Evidence) > 0 {
		fmt.Fprintln(out, "evidence:")
		for _, line := range run.Tune.Recommendation.Evidence {
			fmt.Fprintf(out, "  - %s\n", line)
		}
		fmt.Fprintln(out)
	}
	if run.Tune.Recommendation.Config != "" {
		fmt.Fprintln(out, "recommended config (add under the mount that gains):")
		for _, line := range strings.Split(run.Tune.Recommendation.Config, "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	return nil
}

func formatTuneSize(value int64) string {
	switch {
	case value >= 1<<20 && value%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", value>>20)
	case value >= 1<<10 && value%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", value>>10)
	default:
		return fmt.Sprintf("%d B", value)
	}
}

func formatTuneRate(bps float64) string {
	if bps >= 1e6 {
		return fmt.Sprintf("%.1f MB/s", bps/1e6)
	}
	return fmt.Sprintf("%.0f KB/s", bps/1e3)
}
