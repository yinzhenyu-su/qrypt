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
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
		Mounts:        []vfs.MountSnapshot{{Identity: vfs.MountSnapshotIdentity{Name: "default"}}},
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
	report := collectDebugAIReport(reportCtx, "collect", "", "/dst/file.bin", 100, []string{"local"}, false)
	if report.TransferContext != nil {
		t.Fatalf("transfer context should be omitted without source: %+v", report.TransferContext)
	}
	for _, item := range report.Errors {
		if strings.HasPrefix(item.Endpoint, "/v1/transfer/context") {
			t.Fatalf("unexpected transfer context request without source: %+v", report.Errors)
		}
	}
}

func TestDebugCollectDiagnosticsReportsRootIDMismatch(t *testing.T) {
	report := debugAIReport{
		Health: &control.HealthResponse{OK: true},
		State: &vfs.DebugSnapshot{
			Kind: "vfs",
			Mounts: []vfs.MountSnapshot{{
				Identity: vfs.MountSnapshotIdentity{
					Name:       "cloud",
					DriverName: "189",
					RootID:     "0",
					Driver: &drive.DebugSnapshot{
						Driver: "189",
						Stats: map[string]any{
							drive.DebugStatRootID: "-11",
						},
					},
				},
			}},
		},
	}
	var diagnostics []debugAIDiagnostic
	addCollectDiagnostics(&diagnostics, report)

	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one root mismatch", diagnostics)
	}
	got := diagnostics[0]
	if got.Code != "root_id_mismatch" || got.Severity != "error" || got.Mount != "cloud" {
		t.Fatalf("unexpected diagnostic: %+v", got)
	}
	if got.Evidence["vfs_root_id"] != "0" || got.Evidence["driver_root_id"] != "-11" {
		t.Fatalf("unexpected root mismatch evidence: %+v", got.Evidence)
	}
}

func TestDebugCollectDiagnosticsPendingJournalDuplicates(t *testing.T) {
	report := debugAIReport{
		Health: &control.HealthResponse{OK: true},
		Cache: &control.CacheResponse{
			Mounts: []control.DebugCacheMountStatus{{
				Mount: "cloud",
				Cache: vfs.DebugReadCache{
					Journal: &vfs.DebugJournal{
						Path:               "/tmp/pending.jsonl",
						Exists:             true,
						Bytes:              512 << 10,
						Entries:            1903,
						PendingCount:       1,
						DuplicateEntries:   1902,
						CompactRecommended: true,
						LargestPaths: []vfs.DebugJournalPath{{
							Path:             "/qrypt.log",
							Entries:          1903,
							DuplicateEntries: 1902,
							LatestSize:       30588928,
							StagingSize:      30572544,
							StagingExists:    true,
							SizeMatches:      false,
							LastJournalOp:    "dirty",
							LastJournalLine:  1903,
						}},
					},
				},
			}},
		},
	}
	var diagnostics []debugAIDiagnostic
	addCollectDiagnostics(&diagnostics, report)

	var compact, duplicate bool
	for _, item := range diagnostics {
		if item.Code == "pending_journal_compaction_recommended" && item.Mount == "cloud" {
			compact = true
		}
		if item.Code == "pending_journal_duplicate_path" && item.Path == "/qrypt.log" {
			duplicate = true
		}
	}
	if !compact || !duplicate {
		t.Fatalf("diagnostics = %+v, want compact and duplicate journal diagnostics", diagnostics)
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
	root.SetArgs([]string{"debug", "collect", "--socket", "/tmp/qrypt.sock"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "specify --mount NAME or --all-mounts") {
		t.Fatalf("expected debug collect without mount scope to fail clearly, got %v", err)
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

	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "auth", Source: "src"}); err == nil {
		t.Fatal("expected auth test with --source to fail")
	}
	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "fs"}); err == nil || !strings.Contains(err.Error(), "fs test requires --mount") {
		t.Fatalf("expected fs test without mount to fail clearly, got %v", err)
	}
	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "resume"}); err == nil || !strings.Contains(err.Error(), "resume test requires --mount") {
		t.Fatalf("expected resume test without mount to fail clearly, got %v", err)
	}
	if err := validateDriverTestRequest(control.DriverTestRequest{Test: "resume", Mount: "mem", Source: "src"}); err == nil ||
		!strings.Contains(err.Error(), "resume test only supports --mount and --size") {
		t.Fatalf("expected resume test with source to fail clearly, got %v", err)
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "crud", Samples: 1}); err != nil {
		t.Fatalf("expected crud benchmark request to be valid: %v", err)
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "fs", Mount: "mem", Samples: 1}); err != nil {
		t.Fatalf("expected fs benchmark request to be valid: %v", err)
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "fs", Samples: 1}); err == nil ||
		!strings.Contains(err.Error(), "fs benchmark requires --mount") {
		t.Fatalf("expected fs benchmark without mount to fail clearly, got %v", err)
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "crud", Samples: 0}); err == nil {
		t.Fatal("expected benchmark with zero samples to fail")
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "xfer"}); err == nil ||
		!strings.Contains(err.Error(), "xfer benchmark requires --source and --dest") {
		t.Fatalf("expected xfer benchmark without source/dest to fail clearly, got %v", err)
	}
	if err := validateDriverBenchRequest(control.DriverTestRequest{Test: "xfer", Source: "src", Dest: "dst", Samples: 1}); err != nil {
		t.Fatalf("expected xfer benchmark request to be valid: %v", err)
	}

	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.json")
	currentPath := filepath.Join(dir, "current.json")
	baseReport := control.BenchmarkReport{
		SchemaVersion: control.BenchmarkSchemaVersion,
		Kind:          "driver_crud_benchmark",
		Mount:         "mem",
		Driver:        "memory",
		Pass:          true,
		Summary:       control.BenchmarkSummary{TotalCases: 1, PassedCases: 1, EventCount: 2},
	}
	currentReport := baseReport
	currentReport.Summary.EventCount = 1
	for path, value := range map[string]control.BenchmarkReport{basePath: baseReport, currentPath: currentReport} {
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
