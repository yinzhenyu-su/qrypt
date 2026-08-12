package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

func TestRootSuppressesUsageForRuntimeErrors(t *testing.T) {
	if !NewRootCommand().SilenceUsage {
		t.Fatal("root command must suppress usage for runtime errors")
	}
}

func TestDebugRequiredFlags(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"debug", "collect"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug collect without --socket to fail")
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "collect", "--socket", "/tmp/qrypt.sock"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "specify --mount NAME or --all-mounts") {
		t.Fatalf("expected debug collect without mount scope to fail clearly, got %v", err)
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "collect", "--url", "http://127.0.0.1:19090"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "specify --mount NAME or --all-mounts") {
		t.Fatalf("expected debug collect with --url but without mount scope to fail clearly, got %v", err)
	}

	debugConfigPath := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(debugConfigPath, []byte(`
[debug]
enabled = true
listen = "127.0.0.1:19090"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	root = NewRootCommand()
	root.SetArgs([]string{"debug", "collect", "--config", debugConfigPath})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "specify --mount NAME or --all-mounts") {
		t.Fatalf("expected debug collect to use configured debug endpoint, got %v", err)
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "raw", "health", "--socket", "/tmp/qrypt.sock", "--url", "http://127.0.0.1:19090"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected --socket and --url conflict, got %v", err)
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "bundle", "--socket", "/tmp/qrypt.sock", "--all-mounts"})
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

	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.json")
	currentPath := filepath.Join(dir, "current.json")
	baseReport := contracttest.BenchmarkReport{
		SchemaVersion: contracttest.BenchmarkSchemaVersion,
		Kind:          "driver_crud_benchmark",
		Mount:         "mem",
		Driver:        "memory",
		Pass:          true,
		Summary:       contracttest.BenchmarkSummary{TotalCases: 1, PassedCases: 1, EventCount: 2},
	}
	currentReport := baseReport
	currentReport.Summary.EventCount = 1
	for path, value := range map[string]contracttest.BenchmarkReport{basePath: baseReport, currentPath: currentReport} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root = NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"debug", "bench", "compare", "--base", basePath, "--current", currentPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("debug bench compare failed: %v", err)
	}
	if !strings.Contains(out.String(), `"kind": "benchmark_comparison"`) ||
		!strings.Contains(out.String(), `"summary.event_count"`) {
		t.Fatalf("unexpected compare output: %s", out.String())
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
