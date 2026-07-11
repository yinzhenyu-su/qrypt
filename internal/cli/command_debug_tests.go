package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/internal/control"
)

func newDebugTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run write-capable debug tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(withDebugSocketFlag(newDebugTestCaseCmd("crud", "Run a CRUD driver test")))
	cmd.AddCommand(withDebugSocketFlag(newDebugTestCaseCmd("instantupload", "Run an instant-upload driver test")))
	cmd.AddCommand(withDebugSocketFlag(newDebugTestCaseCmd("xfer", "Run a transfer driver test")))
	return cmd
}

func newDebugTestCaseCmd(test, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:               test,
		Short:             short,
		Args:              cobra.NoArgs,
		ValidArgsFunction: noFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDebugDriverTest(cmd, test)
		},
	}
	addDebugDriverTestFlags(cmd, test)
	return cmd
}

func addDebugDriverTestFlags(cmd *cobra.Command, test string) {
	if test == "" || test == "crud" || test == "instantupload" {
		cmd.Flags().String("mount", "", "mount name for crud or instantupload tests")
	}
	if test == "" || test == "xfer" {
		cmd.Flags().String("source", "", "source mount for xfer test")
		cmd.Flags().String("dest", "", "destination mount for xfer test")
		cmd.Flags().String("size", "", "xfer test size in bytes, or k/m/g suffix")
		cmd.Flags().Bool("vfs", false, "run xfer test through the VFS layer")
	}
}

func runDebugDriverTest(cmd *cobra.Command, test string) error {
	req := control.DriverTestRequest{Test: strings.ToLower(test)}
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
	if flag := cmd.Flags().Lookup("vfs"); flag != nil {
		req.VFS, _ = cmd.Flags().GetBool("vfs")
	}
	if err := validateDriverTestRequest(req); err != nil {
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

func validateDriverTestRequest(req control.DriverTestRequest) error {
	switch req.Test {
	case "crud", "instantupload":
		if req.Source != "" || req.Dest != "" || req.Size != "" || req.VFS {
			return fmt.Errorf("%s test only supports --mount", req.Test)
		}
	case "xfer":
		if req.Mount != "" {
			return fmt.Errorf("xfer test uses --source and --dest, not --mount")
		}
		if req.Source == "" || req.Dest == "" {
			return fmt.Errorf("xfer test requires --source and --dest")
		}
		if req.Source == req.Dest {
			return fmt.Errorf("xfer test requires different source and dest mounts")
		}
	default:
		return fmt.Errorf("unknown driver test %q", req.Test)
	}
	return nil
}
