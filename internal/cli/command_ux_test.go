package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/internal/control"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

func TestRootSuppressesUsageForRuntimeErrors(t *testing.T) {
	if !NewRootCommand().SilenceUsage {
		t.Fatal("root command must suppress usage for runtime errors")
	}
}

func TestDebugBundleFilesIncludeTransferEvidence(t *testing.T) {
	files := debugBundleFiles("/src/file.bin", "/dst/file.bin", false, false)
	for _, name := range []string{
		"destination.json",
		"raw/reads.json",
		"raw/reads-path.json",
		"raw/reads-destination.json",
		"raw/transfer-context.json",
	} {
		if !slices.Contains(files, name) {
			t.Fatalf("bundle files missing %q: %#v", name, files)
		}
	}
}

func TestDebugBundleFilesOmitTransferContextWithoutSource(t *testing.T) {
	files := debugBundleFiles("", "/dst/file.bin", false, false)
	if slices.Contains(files, "raw/transfer-context.json") {
		t.Fatalf("bundle files should omit transfer context without source: %#v", files)
	}
}

type debugReportTestSource struct{}

func (debugReportTestSource) DebugSnapshot() vfs.DebugSnapshot {
	return vfs.DebugSnapshot{
		SchemaVersion: vfs.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "vfs",
		Mounts:        []vfs.DebugMountSnapshot{{Name: "default"}},
	}
}

func TestDebugCollectOmitsTransferContextWithoutSource(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), "qrypt-test-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".sock")
	defer os.Remove(socketPath)
	server, err := control.NewServer(socketPath, debugReportTestSource{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer server.Close(context.Background())

	reportCtx := context.WithValue(context.Background(), debugSocketContextKey{}, socketPath)
	report := collectDebugAIReport(reportCtx, "collect", "", "/dst/file.bin", 100)
	if report.TransferContext != nil {
		t.Fatalf("transfer context should be omitted without source: %+v", report.TransferContext)
	}
	for _, item := range report.Errors {
		if strings.HasPrefix(item.Endpoint, "/v1/transfer/context") {
			t.Fatalf("unexpected transfer context request without source: %+v", report.Errors)
		}
	}
}

func TestDriverListJSON(t *testing.T) {
	cmd := newDriverListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(out.Bytes(), &names); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected registered drivers")
	}
}

func TestDebugRequiredFlags(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"debug", "collect"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug collect without --socket to fail")
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "bundle", "--socket", "/tmp/qrypt.sock"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug bundle without --out to fail")
	}

	root = NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"debug", "test"})
	if err := root.Execute(); err != nil {
		t.Fatalf("expected debug test without subcommand to show help: %v", err)
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "test", "crud"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug test without --socket to fail")
	}

	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "auth", Source: "src"}); err == nil {
		t.Fatal("expected auth test with --source to fail")
	}
	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "fs"}); err == nil || !strings.Contains(err.Error(), "fs test requires --mount") {
		t.Fatalf("expected fs test without mount to fail clearly, got %v", err)
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "test", "xfer", "--socket", "/tmp/qrypt.sock"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected xfer test without source/dest to fail")
	}
}

func TestReadableCommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "removed inspect",
			args: []string{"debug", "inspect"},
			want: "debug inspect was removed; use 'qrypt debug collect REMOTE --socket PATH'",
		},
		{
			name: "missing debug socket",
			args: []string{"debug", "collect"},
			want: "--socket PATH is required for runtime debug commands",
		},
		{
			name: "missing fs copy args",
			args: []string{"fs", "copy"},
			want: "missing SOURCE and DESTINATION",
		},
		{
			name: "missing fs copy destination",
			args: []string{"fs", "copy", "/src"},
			want: "missing DESTINATION",
		},
		{
			name: "missing bundle output",
			args: []string{"debug", "bundle", "--socket", "/tmp/qrypt.sock"},
			want: "missing --out FILE",
		},
		{
			name: "unknown fs subcommand",
			args: []string{"fs", "copie"},
			want: `unknown command "copie" for "qrypt fs"`,
		},
		{
			name: "removed debug driver",
			args: []string{"debug", "driver"},
			want: "debug driver was removed",
		},
		{
			name: "unknown flag",
			args: []string{"fs", "list", "--bad"},
			want: "Run 'qrypt fs list --help' for valid flags.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRootCommand()
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(tt.args)
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for args %#v", tt.args)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.want)
			}
		})
	}
}
