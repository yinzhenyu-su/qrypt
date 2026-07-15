package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestRunDriverCRUDTestReportsContractMatrixAndCleanup(t *testing.T) {
	driver := newCRUDMemoryDriver()
	result := RunDriverCRUDTest(context.Background(), "mem", driver)
	if !result.Pass {
		t.Fatalf("crud test pass = false, steps=%#v cleanup=%#v residual=%#v", result.Steps, result.Cleanup, result.Residual)
	}
	if result.OpID == "" || result.RetryCommand == "" || result.Duration == "" {
		t.Fatalf("result missing diagnostic metadata: %+v", result)
	}
	if result.DurationMS <= 0 {
		t.Fatalf("result duration_ms = %d, want positive", result.DurationMS)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !bytes.Contains(body, []byte(`"duration_ms"`)) || !bytes.Contains(body, []byte(`"elapsed_ms"`)) {
		t.Fatalf("result JSON missing machine-comparable duration fields: %s", body)
	}
	if len(result.Created) < 8 {
		t.Fatalf("created artifacts = %d, want test dir, nested dir, and matrix files: %#v", len(result.Created), result.Created)
	}
	if len(result.Cleanup) == 0 {
		t.Fatalf("cleanup report is empty")
	}
	if len(result.Residual) != 0 {
		t.Fatalf("unexpected residual artifacts: %#v", result.Residual)
	}
	if len(result.ResidualTimeline) == 0 {
		t.Fatalf("residual timeline is empty")
	}
	if len(result.Metrics) == 0 {
		t.Fatalf("metrics is empty")
	}
	seenOps := map[string]bool{}
	seenNames := map[string]bool{}
	for _, step := range result.Steps {
		if step.OpID != result.OpID || step.Mount != "mem" || step.Driver != "memory" {
			t.Fatalf("step missing unified metadata: %+v", step)
		}
		if step.Input == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing input: %+v", step.Operation, step.Name, step)
		}
		if step.Expected == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing expected values: %+v", step.Operation, step.Name, step)
		}
		if step.Actual == nil && (step.Operation == "put" || step.Operation == "read" || step.Operation == "verify_put_list") {
			t.Fatalf("step %s/%s missing actual values: %+v", step.Operation, step.Name, step)
		}
		seenOps[step.Operation] = true
		seenNames[step.Name] = true
	}
	for _, op := range []string{"mkdir", "verify_mkdir_list", "mkdir_nested", "put", "verify_put_list", "read", "rename", "verify_rename_list", "verify_cleanup_list"} {
		if !seenOps[op] {
			t.Fatalf("missing operation %q in steps: %#v", op, result.Steps)
		}
	}
	for _, name := range []string{"empty.bin", "one-byte.bin", "space name.txt", "unicode-中文.txt", "nested.txt"} {
		if !seenNames[name] {
			t.Fatalf("missing matrix case %q in steps: %#v", name, result.Steps)
		}
	}
}

func TestCRUDBenchmarkReportSummarizesResult(t *testing.T) {
	driver := newCRUDMemoryDriver()
	result := RunDriverCRUDTest(context.Background(), "mem", driver)
	report := NewCRUDBenchmarkReport(*result)

	if report.SchemaVersion != BenchmarkSchemaVersion || report.Kind != "driver_crud_benchmark" {
		t.Fatalf("unexpected benchmark identity: %+v", report)
	}
	if !report.Pass || report.Mount != "mem" || report.Driver != "memory" {
		t.Fatalf("unexpected benchmark result metadata: %+v", report)
	}
	if report.Summary.TotalCases == 0 || report.Summary.PassedCases == 0 || report.Summary.FailedCases != 0 {
		t.Fatalf("unexpected benchmark summary: %+v", report.Summary)
	}
	if report.Summary.EventCount == 0 || len(report.Events) == 0 {
		t.Fatalf("benchmark report missing metric events: %+v", report.Summary)
	}
	raw, ok := report.Raw.([]CRUDTestResult)
	if len(report.Cases) == 0 || !ok || len(raw) != 1 {
		t.Fatalf("benchmark report missing cases/raw result: cases=%d raw=%#v", len(report.Cases), report.Raw)
	}
	if report.Assessment.Status != "pass" || report.Assessment.PerformanceComparable {
		t.Fatalf("unexpected assessment: %+v", report.Assessment)
	}
}

func TestCRUDBenchmarkReportAggregatesSamples(t *testing.T) {
	driver := newCRUDMemoryDriver()
	first := *RunDriverCRUDTest(context.Background(), "mem", driver)
	second := first
	third := first
	first.DurationMS = 100
	second.DurationMS = 110
	third.DurationMS = 120
	second.OpID = first.OpID + "-2"
	third.OpID = first.OpID + "-3"

	report := NewCRUDBenchmarkReportSamplesWithEnvironment([]CRUDTestResult{first, second, third}, BenchmarkEnvironment{
		NetworkProbe: &BenchmarkNetworkProbe{Status: "ok"},
	})
	raw, ok := report.Raw.([]CRUDTestResult)
	if len(report.Samples) != 3 || !ok || len(raw) != 3 {
		t.Fatalf("unexpected sample/raw counts: samples=%d raw=%#v", len(report.Samples), report.Raw)
	}
	if report.Summary.SampleCount != 3 || report.Summary.PassedSamples != 3 || report.Summary.FailedSamples != 0 {
		t.Fatalf("unexpected sample summary: %+v", report.Summary)
	}
	if report.Summary.DurationStats.Count != 3 || report.Summary.DurationStats.Median != 110 {
		t.Fatalf("unexpected duration stats: %+v", report.Summary.DurationStats)
	}
	if report.Summary.Operations["put"].Count == 0 || report.Summary.Operations["read"].Count == 0 {
		t.Fatalf("benchmark summary missing CRUD operation stats: %+v", report.Summary.Operations)
	}
	if report.Summary.EventOperationSummaries["memory"].Count == 0 {
		t.Fatalf("benchmark summary missing event operation stats: %+v", report.Summary.EventOperationSummaries)
	}
	if !report.Assessment.PerformanceComparable {
		t.Fatalf("expected stable samples to be performance comparable: %+v", report.Assessment)
	}
}

func TestFSBenchmarkReportSummarizesSmokeResult(t *testing.T) {
	result := FSTestResult{
		OpID:       "fs-1",
		Mount:      "mem",
		Pass:       true,
		Started:    time.Unix(1, 0),
		Finished:   time.Unix(1, int64(200*time.Millisecond)),
		Duration:   "200ms",
		DurationMS: 200,
		Steps: []FSTestStep{
			{Operation: "write", OK: true, Duration: "50ms", DurationMS: 50, Input: map[string]any{"bytes": 32}},
			{Operation: "read", OK: true, Duration: "40ms", DurationMS: 40, Actual: map[string]any{"bytes": 32}},
			{Operation: "wait_upload", OK: true, Duration: "100ms", DurationMS: 100},
		},
		PendingTimeline: []FSPendingSample{{PendingCount: 2, UploadCount: 1, DeleteTimers: 0}},
		FinalState: &FSMountState{
			Mount:                 "mem",
			CacheHits:             3,
			CacheMisses:           1,
			CacheHitRatio:         0.75,
			StagingOrphans:        0,
			StagingSizeMismatches: 0,
		},
	}
	report := NewFSBenchmarkReportSamplesWithEnvironment([]FSTestResult{result}, BenchmarkEnvironment{NetworkProbe: &BenchmarkNetworkProbe{Status: "ok"}})
	if report.Kind != "vfs_fs_benchmark" || !report.Pass || report.Mount != "mem" {
		t.Fatalf("unexpected fs benchmark metadata: %+v", report)
	}
	raw, ok := report.Raw.([]FSTestResult)
	if !ok || len(raw) != 1 {
		t.Fatalf("unexpected fs benchmark raw result: %#v", report.Raw)
	}
	if report.Summary.BytesRead != 32 || report.Summary.BytesWritten != 32 {
		t.Fatalf("unexpected fs benchmark bytes: %+v", report.Summary)
	}
	if report.Summary.Operations["wait_upload"].Count != 1 {
		t.Fatalf("fs benchmark missing operation summary: %+v", report.Summary.Operations)
	}
	if report.Summary.VFS == nil || report.Summary.VFS.PendingMax != 2 || report.Summary.VFS.UploadMax != 1 || report.Summary.VFS.CacheHitRatio != 0.75 {
		t.Fatalf("fs benchmark missing VFS summary: %+v", report.Summary.VFS)
	}
}

func TestXferBenchmarkReportSummarizesTransferResult(t *testing.T) {
	result := XferTestResult{
		OpID:        "xfer-1",
		SourceMount: "local",
		DestMount:   "cloud",
		SourceType:  "localfs",
		DestType:    "memory",
		VFS:         true,
		Pass:        true,
		Started:     time.Unix(1, 0),
		Finished:    time.Unix(1, int64(500*time.Millisecond)),
		Metrics: XferTestMetrics{
			TotalBytes:      64,
			WallTime:        "500ms",
			WallMS:          500,
			ReadThroughput:  640,
			WriteThroughput: 320,
		},
		Steps: []TransferStep{
			{Phase: "staging_write_source", OK: true, Duration: "200ms", DurationMS: 200, Bytes: 64},
			{Phase: "read_source", OK: true, Duration: "100ms", DurationMS: 100, Bytes: 64},
			{Phase: "staging_write_dest", OK: true, Duration: "200ms", DurationMS: 200, Bytes: 64},
			{Phase: "verify_data", OK: true, Duration: "50ms", DurationMS: 50, Bytes: 64},
		},
		Timeline: []drive.MetricEvent{{
			Operation:  "upload",
			OK:         true,
			DurationMS: 200,
			Bytes:      64,
		}},
	}

	report := NewXferBenchmarkReportSamplesWithEnvironment([]XferTestResult{result}, BenchmarkEnvironment{NetworkProbe: &BenchmarkNetworkProbe{Status: "ok"}})

	if report.Kind != "xfer_benchmark" || !report.Pass || !report.VFS {
		t.Fatalf("unexpected xfer benchmark metadata: %+v", report)
	}
	if report.SourceMount != "local" || report.DestMount != "cloud" || report.SourceDriver != "localfs" || report.DestDriver != "memory" {
		t.Fatalf("unexpected xfer benchmark identity: %+v", report)
	}
	raw, ok := report.Raw.([]XferTestResult)
	if !ok || len(raw) != 1 {
		t.Fatalf("unexpected xfer benchmark raw result: %#v", report.Raw)
	}
	if report.DurationMS != 500 || report.Summary.BytesRead != 64 || report.Summary.BytesWritten != 128 {
		t.Fatalf("unexpected xfer benchmark summary: duration=%d summary=%+v", report.DurationMS, report.Summary)
	}
	if report.Summary.ReadBPS != 640 || report.Summary.WriteBPS != 320 {
		t.Fatalf("unexpected xfer throughput summary: %+v", report.Summary)
	}
	if report.Summary.Operations["read_source"].Count != 1 || report.Summary.Operations["staging_write_dest"].Count != 1 {
		t.Fatalf("xfer benchmark missing operation summaries: %+v", report.Summary.Operations)
	}
	if report.Summary.EventOperationSummaries["upload"].Count != 1 {
		t.Fatalf("xfer benchmark missing event operation summaries: %+v", report.Summary.EventOperationSummaries)
	}
}

func TestBenchmarkNetworkProbeRecordsReadOnlyDriverSignals(t *testing.T) {
	driver := newCRUDMemoryDriver()
	probe := RunBenchmarkNetworkProbe(context.Background(), driver)
	if probe.Status == "" || probe.DurationMS <= 0 {
		t.Fatalf("unexpected probe metadata: %+v", probe)
	}
	if len(probe.Steps) == 0 || probe.APILatency.Count == 0 {
		t.Fatalf("probe missing steps or latency stats: %+v", probe)
	}
	if probe.EventCount == 0 || len(probe.Events) == 0 {
		t.Fatalf("probe missing metric events: %+v", probe)
	}
	if probe.ErrorCount != 0 {
		t.Fatalf("probe reported unexpected errors: %+v", probe)
	}
}

func TestCompareBenchmarkReportsFlagsStructuralRegressions(t *testing.T) {
	base := BenchmarkReport{
		Kind:   "driver_crud_benchmark",
		Mount:  "mem",
		Driver: "memory",
		Pass:   true,
		Summary: BenchmarkSummary{
			TotalCases:       10,
			PassedCases:      10,
			EventCount:       5,
			EventOperations:  map[string]int{"api_request": 4, "download": 1},
			CleanupResiduals: 0,
		},
	}
	current := base
	current.Pass = false
	current.Summary.FailedCases = 1
	current.Summary.ErrorCount = 1
	current.Summary.EventCount = 3
	current.Summary.EventOperations = map[string]int{"api_request": 2}

	report := CompareBenchmarkReports(base, current)
	if report.Pass || report.Status != "fail" {
		t.Fatalf("comparison should fail: %+v", report)
	}
	seen := map[string]bool{}
	for _, diff := range report.Differences {
		seen[diff.Metric] = true
	}
	for _, metric := range []string{"pass", "summary.error_count", "summary.failed_cases", "summary.event_count", "summary.event_operations.api_request", "summary.event_operations.download"} {
		if !seen[metric] {
			t.Fatalf("missing comparison metric %q in %+v", metric, report.Differences)
		}
	}
}

func TestCompareBenchmarkReportsFlagsPerformanceRegressionsWhenComparable(t *testing.T) {
	base := comparableBenchmarkReport()
	current := comparableBenchmarkReport()
	current.Summary.DurationStats.Median = 140
	current.Summary.DurationStats.P95 = 180
	current.Summary.ReadBPSStats.Median = 700
	current.Summary.WriteBPSStats.Median = 700
	current.Summary.Operations["put"] = BenchmarkPhaseSummary{Count: 3, DurationStats: BenchmarkStats{Median: 140, P95: 180}}
	current.Summary.EventOperationSummaries["api_request"] = BenchmarkPhaseSummary{Count: 3, DurationStats: BenchmarkStats{Median: 140, P95: 180}}

	report := CompareBenchmarkReports(base, current)
	if !report.Pass || report.Status != "warning" {
		t.Fatalf("comparison should warn without failing: %+v", report)
	}
	seen := map[string]bool{}
	for _, diff := range report.Differences {
		seen[diff.Metric] = true
	}
	for _, metric := range []string{
		"summary.duration_ms_stats.median",
		"summary.duration_ms_stats.p95",
		"summary.read_bps_stats.median",
		"summary.write_bps_stats.median",
		"summary.operations.put.duration_ms_stats.median",
		"summary.event_operation_summaries.api_request.duration_ms_stats.median",
	} {
		if !seen[metric] {
			t.Fatalf("missing performance metric %q in %+v", metric, report.Differences)
		}
	}
}

func TestCompareBenchmarkReportsSkipsPerformanceWhenNotComparable(t *testing.T) {
	base := comparableBenchmarkReport()
	current := comparableBenchmarkReport()
	current.Assessment.PerformanceComparable = false

	report := CompareBenchmarkReports(base, current)
	seen := map[string]bool{}
	for _, diff := range report.Differences {
		seen[diff.Metric] = true
	}
	if !seen["performance.comparable"] {
		t.Fatalf("comparison should report inconclusive performance: %+v", report.Differences)
	}
	if seen["summary.duration_ms_stats.median"] {
		t.Fatalf("comparison should not emit latency regression when not comparable: %+v", report.Differences)
	}
}

func TestCompareBenchmarkReportsFlagsVFSRegressions(t *testing.T) {
	base := comparableBenchmarkReport()
	current := comparableBenchmarkReport()
	base.Summary.VFS = &BenchmarkVFSSummary{PendingMax: 1, CacheHitRatio: 0.8}
	current.Summary.VFS = &BenchmarkVFSSummary{
		PendingMax:            3,
		PendingFinal:          1,
		StagingOrphans:        1,
		StagingSizeMismatches: 1,
		CacheErrors:           1,
		CacheHitRatio:         0.4,
		PendingDrainMS:        200,
	}

	report := CompareBenchmarkReports(base, current)
	if report.Pass || report.Status != "fail" {
		t.Fatalf("comparison should fail for pending_final: %+v", report)
	}
	seen := map[string]bool{}
	for _, diff := range report.Differences {
		seen[diff.Metric] = true
	}
	for _, metric := range []string{
		"summary.vfs.pending_final",
		"summary.vfs.staging_orphans",
		"summary.vfs.staging_size_mismatches",
		"summary.vfs.cache_errors",
		"summary.vfs.pending_max",
		"summary.vfs.cache_hit_ratio",
	} {
		if !seen[metric] {
			t.Fatalf("missing VFS metric %q in %+v", metric, report.Differences)
		}
	}
}

func comparableBenchmarkReport() BenchmarkReport {
	return BenchmarkReport{
		Kind:   "driver_crud_benchmark",
		Mount:  "mem",
		Driver: "memory",
		Pass:   true,
		Assessment: BenchmarkAssessment{
			Status:                "pass",
			Confidence:            "medium",
			PerformanceComparable: true,
		},
		Environment: BenchmarkEnvironment{NetworkProbe: &BenchmarkNetworkProbe{Status: "ok"}},
		Summary: BenchmarkSummary{
			SampleCount: 3,
			DurationStats: BenchmarkStats{
				Count:  3,
				Median: 100,
				P95:    120,
			},
			ReadBPSStats: BenchmarkStats{
				Count:  3,
				Median: 1000,
			},
			WriteBPSStats: BenchmarkStats{
				Count:  3,
				Median: 1000,
			},
			Operations: map[string]BenchmarkPhaseSummary{
				"put": {Count: 3, DurationStats: BenchmarkStats{Median: 100, P95: 120}},
			},
			EventOperationSummaries: map[string]BenchmarkPhaseSummary{
				"api_request": {Count: 3, DurationStats: BenchmarkStats{Median: 100, P95: 120}},
			},
		},
	}
}

type crudMemoryDriver struct {
	drive.UnsupportedOperations
	entries map[string]drive.Entry
	data    map[string][]byte
	child   map[string][]string
	next    int
	listErr error
}

func newCRUDMemoryDriver() *crudMemoryDriver {
	d := &crudMemoryDriver{
		entries: map[string]drive.Entry{},
		data:    map[string][]byte{},
		child:   map[string][]string{},
	}
	d.entries["root"] = drive.Entry{ID: "root", Name: "", IsDir: true}
	return d
}

func (d *crudMemoryDriver) Init(context.Context) error { return nil }
func (d *crudMemoryDriver) Drop(context.Context) error { return nil }

func (d *crudMemoryDriver) List(_ context.Context, parentID string) ([]drive.Entry, error) {
	if d.listErr != nil {
		return nil, d.listErr
	}
	if parentID == "" {
		parentID = "root"
	}
	if _, ok := d.entries[parentID]; !ok {
		return nil, fmt.Errorf("parent %q not found", parentID)
	}
	ids := append([]string(nil), d.child[parentID]...)
	sort.Strings(ids)
	entries := make([]drive.Entry, 0, len(ids))
	for _, id := range ids {
		if entry, ok := d.entries[id]; ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (d *crudMemoryDriver) Read(_ context.Context, entry drive.Entry, _, _ int64) (io.ReadCloser, error) {
	data, ok := d.data[entry.ID]
	if !ok {
		return nil, fmt.Errorf("file %q not found", entry.ID)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (d *crudMemoryDriver) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *crudMemoryDriver) Mkdir(_ context.Context, parentID, name string) (drive.Entry, error) {
	if parentID == "" {
		parentID = "root"
	}
	if _, ok := d.entries[parentID]; !ok {
		return drive.Entry{}, fmt.Errorf("parent %q not found", parentID)
	}
	d.next++
	id := fmt.Sprintf("dir-%d", d.next)
	entry := drive.Entry{ID: id, ParentID: parentID, Name: name, IsDir: true, ModTime: time.Now()}
	d.entries[id] = entry
	d.child[parentID] = append(d.child[parentID], id)
	return entry, nil
}

func (d *crudMemoryDriver) Move(_ context.Context, entry drive.Entry, dstParentID string) error {
	if _, ok := d.entries[dstParentID]; !ok {
		return fmt.Errorf("parent %q not found", dstParentID)
	}
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	d.removeChild(current.ParentID, entry.ID)
	current.ParentID = dstParentID
	d.entries[entry.ID] = current
	d.child[dstParentID] = append(d.child[dstParentID], entry.ID)
	return nil
}

func (d *crudMemoryDriver) Rename(_ context.Context, entry drive.Entry, newName string) error {
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	current.Name = newName
	d.entries[entry.ID] = current
	return nil
}

func (d *crudMemoryDriver) Remove(_ context.Context, entry drive.Entry) error {
	current, ok := d.entries[entry.ID]
	if !ok {
		return fmt.Errorf("entry %q not found", entry.ID)
	}
	if current.IsDir && len(d.child[entry.ID]) > 0 {
		return fmt.Errorf("directory %q is not empty", entry.ID)
	}
	d.removeChild(current.ParentID, entry.ID)
	delete(d.entries, entry.ID)
	delete(d.data, entry.ID)
	return nil
}

func (d *crudMemoryDriver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if _, ok := d.entries[req.ParentID]; !ok {
		return drive.Entry{}, fmt.Errorf("parent %q not found", req.ParentID)
	}
	rc, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return drive.Entry{}, err
	}
	d.next++
	id := fmt.Sprintf("file-%d", d.next)
	entry := drive.Entry{ID: id, ParentID: req.ParentID, Name: req.Name, Size: int64(len(data)), ModTime: time.Now()}
	d.entries[id] = entry
	d.data[id] = data
	d.child[req.ParentID] = append(d.child[req.ParentID], id)
	return entry, nil
}

func (d *crudMemoryDriver) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{Driver: "memory"}, nil
}

func (d *crudMemoryDriver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilitySourceUploader,
		drive.CapabilityWriter,
	}
}

func (d *crudMemoryDriver) metricEvents(_ context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return []drive.MetricEvent{{
		At:        since.Add(time.Millisecond),
		OpID:      "crud-test-op",
		Step:      "put",
		Layer:     "driver.http",
		Operation: "memory",
	}}, nil
}

func (d *crudMemoryDriver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("memory", metrics), nil
}

func (d *crudMemoryDriver) ResolvePath(_ context.Context, path string) (string, error) {
	if path == "/" {
		return "root", nil
	}
	return "", fmt.Errorf("path %q not found", path)
}

func (d *crudMemoryDriver) removeChild(parentID, id string) {
	children := d.child[parentID]
	for i, child := range children {
		if child == id {
			d.child[parentID] = append(children[:i], children[i+1:]...)
			return
		}
	}
}
