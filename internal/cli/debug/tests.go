package debug

import (
	"fmt"
	"strings"

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
	if test == "" || test == "auth" || test == "batchmove" || test == "batchupload" || test == "crud" || test == "contract" || test == "fs" || test == "instantupload" || test == "resume" || test == "multipart" {
		cmd.Flags().String("mount", "", "mount name for the single-mount test")
	}
	if test == "fs" || test == "resume" || test == "batchmove" || test == "batchupload" {
		cmd.Flags().String("size", "", "test size in bytes, or k/m/g suffix")
	}
	if test == "batchmove" || test == "batchupload" {
		cmd.Flags().Int("count", 0, fmt.Sprintf("number of files (default %d, max %d)", contracttest.DefaultBatchTestCount, contracttest.MaxBatchTestCount))
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
