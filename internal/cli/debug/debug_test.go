package debug

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/contracttest"
	"github.com/yinzhenyu/qrypt/pkg/control"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/diagnostics"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

func TestDebugBundleFilesIncludeTransferEvidence(t *testing.T) {
	files := DebugBundleFiles("/src/file.bin", "/dst/file.bin", false, false)
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
	files := DebugBundleFiles("", "/dst/file.bin", false, false)
	if slices.Contains(files, "raw/transfer-context.json") {
		t.Fatalf("bundle files should omit transfer context without source: %#v", files)
	}
}

type debugReportTestSource struct{}

func (debugReportTestSource) DebugSnapshot() diagnostics.DebugSnapshot {
	return diagnostics.DebugSnapshot{
		SchemaVersion: diagnostics.DebugSnapshotSchemaVersion,
		GeneratedAt:   time.Unix(1, 0),
		Kind:          "vfs",
		Mounts:        []diagnostics.MountSnapshot{{Identity: diagnostics.MountSnapshotIdentity{Name: "default"}}},
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

	reportCtx := context.WithValue(context.Background(), DebugSocketContextKey{}, socketPath)
	report := CollectDebugAIReport(reportCtx, "collect", "", "/dst/file.bin", 100, []string{"local"}, false)
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
	report := DebugAIReport{
		Health: &control.HealthResponse{OK: true},
		State: &diagnostics.DebugSnapshot{
			Kind: "vfs",
			Mounts: []diagnostics.MountSnapshot{{
				Identity: diagnostics.MountSnapshotIdentity{
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
	var diagnostics []DebugAIDiagnostic
	AddCollectDiagnostics(&diagnostics, report)

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
	report := DebugAIReport{
		Health: &control.HealthResponse{OK: true},
		Cache: &control.CacheResponse{
			Mounts: []control.DebugCacheMountStatus{{
				Mount: "cloud",
				Cache: diagnostics.DebugCacheSnapshot{
					DebugReadCache: readcache.DebugReadCache{},
					Journal: &upload.DebugJournal{
						Path:               "/tmp/pending.jsonl",
						Exists:             true,
						Bytes:              512 << 10,
						Entries:            1903,
						PendingCount:       1,
						DuplicateEntries:   1902,
						CompactRecommended: true,
						LargestPaths: []upload.DebugJournalPath{{
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
	var diagnostics []DebugAIDiagnostic
	AddCollectDiagnostics(&diagnostics, report)

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

func TestValidateDriverTestRequest(t *testing.T) {
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "auth", Source: "src"}); err == nil {
		t.Fatal("expected auth test with --source to fail")
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "fs"}); err == nil || !strings.Contains(err.Error(), "fs test requires --mount") {
		t.Fatalf("expected fs test without mount to fail clearly, got %v", err)
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "resume"}); err == nil || !strings.Contains(err.Error(), "resume test requires --mount") {
		t.Fatalf("expected resume test without mount to fail clearly, got %v", err)
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "resume", Mount: "mem", Source: "src"}); err == nil ||
		!strings.Contains(err.Error(), "resume test only supports --mount and --size") {
		t.Fatalf("expected resume test with source to fail clearly, got %v", err)
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "read", Mount: "mem"}); err == nil || !strings.Contains(err.Error(), "requires --mount-point") {
		t.Fatalf("expected read test without mount point to fail clearly, got %v", err)
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "read", Mount: "mem", MountPoint: "/tmp/mount", Size: "64m", BlockSize: "1m", CacheMode: "both", Samples: 2}); err != nil {
		t.Fatalf("expected valid read request, got %v", err)
	}
	if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: "read", Mount: "mem", MountPoint: "/tmp/mount", CacheMode: "invalid", Samples: 1}); err == nil {
		t.Fatal("expected invalid read cache mode to fail")
	}
	for _, test := range []string{"batchupload", "batchmove"} {
		if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: test}); err == nil || !strings.Contains(err.Error(), "requires --mount") {
			t.Fatalf("expected %s without mount to fail clearly, got %v", test, err)
		}
		if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: test, Mount: "mem", Count: 4, Size: "4k"}); err != nil {
			t.Fatalf("expected valid %s request, got %v", test, err)
		}
		if err := ValidateDriverTestRequest(contracttest.DriverTestRequest{Test: test, Mount: "mem", Count: contracttest.MaxBatchTestCount + 1}); err == nil {
			t.Fatalf("expected oversized %s count to fail", test)
		}
	}
}

func TestValidateDriverBenchRequest(t *testing.T) {
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "crud", Samples: 1}); err != nil {
		t.Fatalf("expected crud benchmark request to be valid: %v", err)
	}
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "fs", Mount: "mem", Samples: 1}); err != nil {
		t.Fatalf("expected fs benchmark request to be valid: %v", err)
	}
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "fs", Samples: 1}); err == nil ||
		!strings.Contains(err.Error(), "fs benchmark requires --mount") {
		t.Fatalf("expected fs benchmark without mount to fail clearly, got %v", err)
	}
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "crud", Samples: 0}); err == nil {
		t.Fatal("expected benchmark with zero samples to fail")
	}
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "xfer"}); err == nil ||
		!strings.Contains(err.Error(), "xfer benchmark requires --source and --dest") {
		t.Fatalf("expected xfer benchmark without source/dest to fail clearly, got %v", err)
	}
	if err := ValidateDriverBenchRequest(contracttest.DriverTestRequest{Test: "xfer", Source: "src", Dest: "dst", Samples: 1}); err != nil {
		t.Fatalf("expected xfer benchmark request to be valid: %v", err)
	}
}
