package debug

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

func NewTestCmd(rt cliruntime.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run write-capable debug tests",
		Args:  cliruntime.CommandGroupArgs(rt, nil),
		RunE:  cliruntime.ShowHelp,
	}
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "auth", "Run a read-only auth driver test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "batchmove", "Move a batch of uploaded files through VFS")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "batchupload", "Upload a batch of small files through VFS")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "crud", "Run a CRUD driver test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "contract", "Run the driver behavioral contract suite")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "fs", "Run a VFS filesystem smoke test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "instantupload", "Run an instant-upload driver test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "multipart", "Run a multipart/chunked upload driver test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "read", "Measure a complete read through a mounted filesystem")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "resume", "Run a VFS resumable-upload test")))
	cmd.AddCommand(withDebugSocketFlag(rt, newDebugTestCaseCmd(rt, "xfer", "Run a transfer driver test")))
	return cmd
}

func newDebugTestCaseCmd(rt cliruntime.Runtime, test, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               test,
		Short:             short,
		Args:              cliruntime.NoArgs(rt),
		ValidArgsFunction: cliruntime.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDebugDriverTest(cmd, test)
		},
	}
	addDebugDriverTestFlags(cmd, test)
	return cmd
}

func addDebugDriverTestFlags(cmd *cobra.Command, test string) {
	if test == "" || test == "auth" || test == "batchmove" || test == "batchupload" || test == "crud" || test == "contract" || test == "fs" || test == "instantupload" || test == "read" || test == "resume" || test == "multipart" {
		cmd.Flags().String("mount", "", "mount name for the single-mount test")
	}
	if test == "fs" || test == "resume" || test == "batchmove" || test == "batchupload" {
		cmd.Flags().String("size", "", "test size in bytes, or k/m/g suffix")
	}
	if test == "batchmove" || test == "batchupload" {
		cmd.Flags().Int("count", 0, fmt.Sprintf("number of files (default %d, max %d)", contracttest.DefaultBatchTestCount, contracttest.MaxBatchTestCount))
	}
	if test == "read" {
		cmd.Flags().String("mount-point", "", "actual FUSE mount directory")
		cmd.Flags().String("block-size", "", "read block size in bytes, or k/m/g suffix (default 1m)")
		cmd.Flags().String("cache-mode", "both", "cache mode: cold, warm, or both")
		cmd.Flags().String("pattern", "sequential", "read pattern: sequential, seek, or both")
		cmd.Flags().Int("samples", 1, fmt.Sprintf("number of read samples (max %d)", contracttest.MaxMountedReadTestSamples))
		cmd.Flags().Int("seek-count", contracttest.DefaultMountedSeekCount, fmt.Sprintf("number of seek probes per sample (max %d)", contracttest.MaxMountedSeekCount))
		cmd.Flags().Duration("seek-overlap-timeout", contracttest.DefaultMountedSeekOverlapTimeout, "maximum wait for an in-flight read to overlap a seek")
		cmd.Flags().String("seek-scenario", "isolated", "seek scenario: isolated, prefetch, or concurrent")
		cmd.Flags().String("seek-size", "", "bytes loaded after each seek, or k/m/g suffix (default 1m)")
		cmd.Flags().Int("seek-warmup-chunks", contracttest.DefaultMountedSeekWarmupChunks, fmt.Sprintf("1 MiB sequential chunks used to create read load (max %d)", contracttest.MaxMountedSeekWarmupChunks))
		cmd.Flags().String("size", "", "generated test file size in bytes, or k/m/g suffix (default 256m)")
	}
	if test == "" || test == "xfer" {
		cmd.Flags().String("source", "", "source mount for xfer test")
		cmd.Flags().String("dest", "", "destination mount for xfer test")
		cmd.Flags().String("size", "", "test size in bytes, or k/m/g suffix")
		cmd.Flags().Bool("vfs", false, "run xfer test through the VFS layer")
	}
	if test == "multipart" {
		cmd.Flags().String("size", "", "test size in bytes, or k/m/g suffix")
	}
}

func runDebugDriverTest(cmd *cobra.Command, test string) error {
	req := contracttest.DriverTestRequest{Test: strings.ToLower(test)}
	if flag := cmd.Flags().Lookup("mount"); flag != nil {
		req.Mount, _ = cmd.Flags().GetString("mount")
	}
	if flag := cmd.Flags().Lookup("source"); flag != nil {
		req.Source, _ = cmd.Flags().GetString("source")
	}
	if flag := cmd.Flags().Lookup("dest"); flag != nil {
		req.Dest, _ = cmd.Flags().GetString("dest")
	}
	if flag := cmd.Flags().Lookup("size"); flag != nil {
		req.Size, _ = cmd.Flags().GetString("size")
	}
	if flag := cmd.Flags().Lookup("count"); flag != nil {
		req.Count, _ = cmd.Flags().GetInt("count")
	}
	if flag := cmd.Flags().Lookup("vfs"); flag != nil {
		req.VFS, _ = cmd.Flags().GetBool("vfs")
	}
	if flag := cmd.Flags().Lookup("mount-point"); flag != nil {
		req.MountPoint, _ = cmd.Flags().GetString("mount-point")
	}
	if flag := cmd.Flags().Lookup("block-size"); flag != nil {
		req.BlockSize, _ = cmd.Flags().GetString("block-size")
	}
	if flag := cmd.Flags().Lookup("cache-mode"); flag != nil {
		req.CacheMode, _ = cmd.Flags().GetString("cache-mode")
	}
	if flag := cmd.Flags().Lookup("pattern"); flag != nil {
		req.ReadPattern, _ = cmd.Flags().GetString("pattern")
	}
	if flag := cmd.Flags().Lookup("samples"); flag != nil {
		req.Samples, _ = cmd.Flags().GetInt("samples")
	}
	if flag := cmd.Flags().Lookup("seek-count"); flag != nil {
		req.SeekCount, _ = cmd.Flags().GetInt("seek-count")
	}
	if flag := cmd.Flags().Lookup("seek-overlap-timeout"); flag != nil {
		value, _ := cmd.Flags().GetDuration("seek-overlap-timeout")
		req.SeekOverlapTimeout = value.String()
	}
	if flag := cmd.Flags().Lookup("seek-scenario"); flag != nil {
		req.SeekScenario, _ = cmd.Flags().GetString("seek-scenario")
	}
	if flag := cmd.Flags().Lookup("seek-size"); flag != nil {
		req.SeekSize, _ = cmd.Flags().GetString("seek-size")
	}
	if flag := cmd.Flags().Lookup("seek-warmup-chunks"); flag != nil {
		req.SeekWarmup, _ = cmd.Flags().GetInt("seek-warmup-chunks")
	}
	if err := ValidateDriverTestRequest(req); err != nil {
		return err
	}
	body, err := debugSocketPostJSON(cmd.Context(), "/v1/driver/test", req)
	if err != nil {
		if strings.Contains(err.Error(), "/v1/driver/test returned status 404") {
			return fmt.Errorf("debug test endpoint is not available on this socket; restart the qrypt mount process with the current binary")
		}
		return err
	}
	_, err = cmd.OutOrStdout().Write(body)
	return err
}

func ValidateDriverTestRequest(req contracttest.DriverTestRequest) error {
	switch req.Test {
	case "auth", "contract", "crud", "instantupload":
		if req.Source != "" || req.Dest != "" || req.Size != "" || req.Count != 0 || req.VFS {
			return fmt.Errorf("%s test only supports --mount", req.Test)
		}
	case "fs", "resume":
		if req.Source != "" || req.Dest != "" || req.VFS || req.Count != 0 {
			return fmt.Errorf("%s test only supports --mount and --size", req.Test)
		}
		if req.Mount == "" {
			return fmt.Errorf("%s test requires --mount\n\nExample:\n  qrypt debug test %s --mount cloud --socket /tmp/qrypt.sock", req.Test, req.Test)
		}
	case "read":
		if req.Source != "" || req.Dest != "" || req.VFS || req.Count != 0 {
			return fmt.Errorf("read test only supports --mount, --mount-point, --size, --block-size, --cache-mode, --pattern, --samples, and seek options")
		}
		if req.Mount == "" {
			return fmt.Errorf("read test requires --mount")
		}
		if strings.TrimSpace(req.MountPoint) == "" {
			return fmt.Errorf("read test requires --mount-point")
		}
		if req.Size != "" {
			size := contracttest.ParseXferSize(req.Size)
			if size < 1 || size > contracttest.MaxMountedReadTestSize {
				return fmt.Errorf("read test --size must be between 1 and %d bytes", contracttest.MaxMountedReadTestSize)
			}
		}
		if req.BlockSize != "" {
			blockSize := contracttest.ParseXferSize(req.BlockSize)
			if blockSize < 1 || blockSize > 16<<20 {
				return fmt.Errorf("read test --block-size must be between 1 and %d bytes", 16<<20)
			}
		}
		if req.Samples < 1 || req.Samples > contracttest.MaxMountedReadTestSamples {
			return fmt.Errorf("read test --samples must be between 1 and %d", contracttest.MaxMountedReadTestSamples)
		}
		if req.SeekCount < 0 || req.SeekCount > contracttest.MaxMountedSeekCount {
			return fmt.Errorf("read test --seek-count must be between 0 and %d", contracttest.MaxMountedSeekCount)
		}
		if req.SeekWarmup < 0 || req.SeekWarmup > contracttest.MaxMountedSeekWarmupChunks {
			return fmt.Errorf("read test --seek-warmup-chunks must be between 0 and %d", contracttest.MaxMountedSeekWarmupChunks)
		}
		if req.SeekOverlapTimeout != "" {
			timeout, err := time.ParseDuration(req.SeekOverlapTimeout)
			if err != nil || timeout < 100*time.Millisecond || timeout > 30*time.Second {
				return fmt.Errorf("read test --seek-overlap-timeout must be between 100ms and 30s")
			}
		}
		if req.SeekSize != "" {
			seekSize := contracttest.ParseXferSize(req.SeekSize)
			if seekSize < 1 || seekSize > 16<<20 {
				return fmt.Errorf("read test --seek-size must be between 1 and %d bytes", 16<<20)
			}
			if req.Size != "" && seekSize > contracttest.ParseXferSize(req.Size) {
				return fmt.Errorf("read test --seek-size must not exceed --size")
			}
		}
		switch strings.ToLower(strings.TrimSpace(req.CacheMode)) {
		case "cold", "warm", "both":
		default:
			return fmt.Errorf("read test --cache-mode must be cold, warm, or both")
		}
		switch strings.ToLower(strings.TrimSpace(req.ReadPattern)) {
		case "", "sequential", "seek", "both":
		default:
			return fmt.Errorf("read test --pattern must be sequential, seek, or both")
		}
		switch strings.ToLower(strings.TrimSpace(req.SeekScenario)) {
		case "", "isolated", "prefetch", "concurrent":
		default:
			return fmt.Errorf("read test --seek-scenario must be isolated, prefetch, or concurrent")
		}
	case "batchmove", "batchupload":
		if req.Source != "" || req.Dest != "" || req.VFS {
			return fmt.Errorf("%s test only supports --mount, --count, and --size", req.Test)
		}
		if req.Mount == "" {
			return fmt.Errorf("%s test requires --mount\n\nExample:\n  qrypt debug test %s --mount cloud --count 50 --size 4k --socket /tmp/qrypt.sock", req.Test, req.Test)
		}
		if req.Count < 0 || req.Count > contracttest.MaxBatchTestCount {
			return fmt.Errorf("%s test --count must be between 1 and %d", req.Test, contracttest.MaxBatchTestCount)
		}
		if req.Size != "" {
			size := contracttest.ParseXferSize(req.Size)
			if size < 1 || size > contracttest.MaxBatchTestSize {
				return fmt.Errorf("%s test --size must be between 1 and %d bytes", req.Test, contracttest.MaxBatchTestSize)
			}
		}
	case "xfer":
		if req.Mount != "" || req.Count != 0 {
			return fmt.Errorf("xfer test uses --source and --dest, not --mount\n\nExample:\n  qrypt debug test xfer --source local --dest cloud --socket /tmp/qrypt.sock")
		}
		if req.Source == "" || req.Dest == "" {
			return fmt.Errorf("xfer test requires --source and --dest\n\nExample:\n  qrypt debug test xfer --source local --dest cloud --socket /tmp/qrypt.sock")
		}
		if req.Source == req.Dest {
			return fmt.Errorf("xfer test requires different source and dest mounts")
		}
	default:
		return fmt.Errorf("unknown driver test %q", req.Test)
	}
	return nil
}
