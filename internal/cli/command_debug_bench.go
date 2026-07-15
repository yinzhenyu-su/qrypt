package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/osutil"
)

func newDebugBenchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Run machine-comparable debug benchmarks",
		Args:  commandGroupArgs(nil),
		RunE:  showHelp,
	}
	cmd.AddCommand(withDebugSocketFlag(newDebugBenchCaseCmd("crud", "Run a CRUD driver benchmark")))
	cmd.AddCommand(withDebugSocketFlag(newDebugBenchCaseCmd("fs", "Run a VFS filesystem benchmark")))
	cmd.AddCommand(withDebugSocketFlag(newDebugBenchCaseCmd("xfer", "Run a transfer benchmark")))
	cmd.AddCommand(newDebugBenchCompareCmd())
	return cmd
}

func newDebugBenchCaseCmd(test, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               test,
		Short:             short,
		Args:              noArgs,
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDebugDriverBench(cmd, test)
		},
	}
	cmd.Flags().String("mount", "", "mount name for benchmark")
	if test == "fs" {
		cmd.Flags().String("size", "", "fs benchmark size in bytes, or k/m/g suffix")
	}
	if test == "xfer" {
		cmd.Flags().String("source", "", "source mount for xfer benchmark")
		cmd.Flags().String("dest", "", "destination mount for xfer benchmark")
		cmd.Flags().String("size", "", "xfer benchmark size in bytes, or k/m/g suffix")
		cmd.Flags().Bool("vfs", false, "run xfer benchmark through the VFS layer")
	}
	cmd.Flags().Int("samples", 1, "number of benchmark samples")
	cmd.Flags().Duration("sample-interval", 0, "delay between benchmark samples")
	return cmd
}

func newDebugBenchCompareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "compare",
		Short:             "Compare two benchmark reports",
		Args:              noArgs,
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			basePath, _ := cmd.Flags().GetString("base")
			currentPath, _ := cmd.Flags().GetString("current")
			if basePath == "" {
				return commandUsageError(cmd, "missing --base FILE")
			}
			if currentPath == "" {
				return commandUsageError(cmd, "missing --current FILE")
			}
			base, err := readBenchmarkReportFile(basePath)
			if err != nil {
				return fmt.Errorf("read base benchmark: %w", err)
			}
			current, err := readBenchmarkReportFile(currentPath)
			if err != nil {
				return fmt.Errorf("read current benchmark: %w", err)
			}
			report := control.CompareBenchmarkReports(base, current)
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		},
	}
	cmd.Flags().String("base", "", "baseline benchmark report JSON")
	cmd.Flags().String("current", "", "current benchmark report JSON")
	return cmd
}

func runDebugDriverBench(cmd *cobra.Command, test string) error {
	req := control.DriverTestRequest{Test: strings.ToLower(test)}
	if flag := cmd.Flags().Lookup("mount"); flag != nil {
		req.Mount, _ = cmd.Flags().GetString("mount")
	}
	if flag := cmd.Flags().Lookup("size"); flag != nil {
		req.Size, _ = cmd.Flags().GetString("size")
	}
	if flag := cmd.Flags().Lookup("source"); flag != nil {
		req.Source, _ = cmd.Flags().GetString("source")
	}
	if flag := cmd.Flags().Lookup("dest"); flag != nil {
		req.Dest, _ = cmd.Flags().GetString("dest")
	}
	if flag := cmd.Flags().Lookup("vfs"); flag != nil {
		req.VFS, _ = cmd.Flags().GetBool("vfs")
	}
	if flag := cmd.Flags().Lookup("samples"); flag != nil {
		req.Samples, _ = cmd.Flags().GetInt("samples")
	}
	if flag := cmd.Flags().Lookup("sample-interval"); flag != nil {
		interval, _ := cmd.Flags().GetDuration("sample-interval")
		req.SampleInterval = interval.String()
	}
	if err := validateDriverBenchRequest(req); err != nil {
		return err
	}
	body, err := debugSocketPostJSON(cmd.Context(), "/v1/bench", req)
	if err != nil {
		if strings.Contains(err.Error(), "/v1/bench returned status 404") {
			return fmt.Errorf("debug bench endpoint is not available on this socket; restart the qrypt mount process with the current binary")
		}
		return err
	}
	_, err = cmd.OutOrStdout().Write(body)
	return err
}

func validateDriverBenchRequest(req control.DriverTestRequest) error {
	switch req.Test {
	case "crud":
		if req.Source != "" || req.Dest != "" || req.Size != "" || req.VFS {
			return fmt.Errorf("crud benchmark only supports --mount")
		}
	case "fs":
		if req.Source != "" || req.Dest != "" || req.VFS {
			return fmt.Errorf("fs benchmark only supports --mount and --size")
		}
		if req.Mount == "" {
			return fmt.Errorf("fs benchmark requires --mount\n\nExample:\n  qrypt debug bench fs --mount cloud --socket /tmp/qrypt.sock")
		}
	case "xfer":
		if req.Mount != "" {
			return fmt.Errorf("xfer benchmark uses --source and --dest, not --mount\n\nExample:\n  qrypt debug bench xfer --source local --dest cloud --socket /tmp/qrypt.sock")
		}
		if req.Source == "" || req.Dest == "" {
			return fmt.Errorf("xfer benchmark requires --source and --dest\n\nExample:\n  qrypt debug bench xfer --source local --dest cloud --socket /tmp/qrypt.sock")
		}
		if req.Source == req.Dest {
			return fmt.Errorf("xfer benchmark requires different source and dest mounts")
		}
	default:
		return fmt.Errorf("unknown benchmark %q", req.Test)
	}
	switch req.Test {
	case "crud", "fs", "xfer":
		if req.Samples < 1 {
			return fmt.Errorf("--samples must be at least 1")
		}
		if req.SampleInterval != "" {
			interval, err := time.ParseDuration(req.SampleInterval)
			if err != nil {
				return err
			}
			if interval < 0 {
				return fmt.Errorf("--sample-interval must not be negative")
			}
		}
	}
	return nil
}

func readBenchmarkReportFile(path string) (control.BenchmarkReport, error) {
	body, err := os.ReadFile(osutil.ExpandHome(path))
	if err != nil {
		return control.BenchmarkReport{}, err
	}
	var report control.BenchmarkReport
	if err := json.Unmarshal(body, &report); err == nil && report.Kind != "" {
		return report, nil
	}
	var reports []control.BenchmarkReport
	if err := json.Unmarshal(body, &reports); err != nil {
		return control.BenchmarkReport{}, err
	}
	switch len(reports) {
	case 0:
		return control.BenchmarkReport{}, fmt.Errorf("benchmark report list is empty")
	case 1:
		return reports[0], nil
	default:
		return control.BenchmarkReport{}, fmt.Errorf("benchmark report list contains %d reports; compare one report at a time", len(reports))
	}
}
