package control

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const BenchmarkSchemaVersion = 1

type BenchmarkReport struct {
	SchemaVersion int                  `json:"schema_version"`
	Kind          string               `json:"kind"`
	Mount         string               `json:"mount,omitempty"`
	Driver        string               `json:"driver,omitempty"`
	SourceMount   string               `json:"source_mount,omitempty"`
	DestMount     string               `json:"dest_mount,omitempty"`
	SourceDriver  string               `json:"source_driver,omitempty"`
	DestDriver    string               `json:"dest_driver,omitempty"`
	VFS           bool                 `json:"vfs,omitempty"`
	Pass          bool                 `json:"pass"`
	Started       time.Time            `json:"started_at,omitempty"`
	Finished      time.Time            `json:"finished_at,omitempty"`
	Duration      string               `json:"duration,omitempty"`
	DurationMS    int64                `json:"duration_ms,omitempty"`
	Summary       BenchmarkSummary     `json:"summary"`
	Assessment    BenchmarkAssessment  `json:"assessment"`
	Environment   BenchmarkEnvironment `json:"environment,omitempty"`
	Samples       []BenchmarkSample    `json:"samples,omitempty"`
	Cases         []BenchmarkCase      `json:"cases,omitempty"`
	Events        []drive.MetricEvent  `json:"events,omitempty"`
	Raw           any                  `json:"raw,omitempty"`
}

type BenchmarkSample struct {
	Index      int                 `json:"index"`
	Pass       bool                `json:"pass"`
	Started    time.Time           `json:"started_at,omitempty"`
	Finished   time.Time           `json:"finished_at,omitempty"`
	Duration   string              `json:"duration,omitempty"`
	DurationMS int64               `json:"duration_ms,omitempty"`
	Summary    BenchmarkSummary    `json:"summary"`
	Assessment BenchmarkAssessment `json:"assessment"`
}

type BenchmarkCompareReport struct {
	SchemaVersion int                   `json:"schema_version"`
	Kind          string                `json:"kind"`
	Pass          bool                  `json:"pass"`
	Status        string                `json:"status"`
	Base          BenchmarkCompareInput `json:"base"`
	Current       BenchmarkCompareInput `json:"current"`
	Differences   []BenchmarkDifference `json:"differences,omitempty"`
}

type BenchmarkCompareInput struct {
	Kind         string `json:"kind"`
	Mount        string `json:"mount,omitempty"`
	Driver       string `json:"driver,omitempty"`
	SourceMount  string `json:"source_mount,omitempty"`
	DestMount    string `json:"dest_mount,omitempty"`
	SourceDriver string `json:"source_driver,omitempty"`
	DestDriver   string `json:"dest_driver,omitempty"`
	VFS          bool   `json:"vfs,omitempty"`
	Pass         bool   `json:"pass"`
	DurationMS   int64  `json:"duration_ms,omitempty"`
}

type BenchmarkDifference struct {
	Metric   string `json:"metric"`
	Severity string `json:"severity"`
	Base     any    `json:"base,omitempty"`
	Current  any    `json:"current,omitempty"`
	Message  string `json:"message,omitempty"`
}

type BenchmarkSummary struct {
	SampleCount             int                              `json:"sample_count,omitempty"`
	PassedSamples           int                              `json:"passed_samples,omitempty"`
	FailedSamples           int                              `json:"failed_samples,omitempty"`
	TotalCases              int                              `json:"total_cases"`
	PassedCases             int                              `json:"passed_cases"`
	FailedCases             int                              `json:"failed_cases"`
	ErrorCount              int                              `json:"error_count"`
	RetryCount              int                              `json:"retry_count"`
	EventCount              int                              `json:"event_count"`
	EventOperations         map[string]int                   `json:"event_operations,omitempty"`
	CleanupResiduals        int                              `json:"cleanup_residuals"`
	BytesRead               int64                            `json:"bytes_read,omitempty"`
	BytesWritten            int64                            `json:"bytes_written,omitempty"`
	ReadBPS                 int64                            `json:"read_bps,omitempty"`
	WriteBPS                int64                            `json:"write_bps,omitempty"`
	P95DurationMS           int64                            `json:"p95_duration_ms,omitempty"`
	MaxDurationMS           int64                            `json:"max_duration_ms,omitempty"`
	DurationStats           BenchmarkStats                   `json:"duration_ms_stats,omitempty"`
	ReadBPSStats            BenchmarkStats                   `json:"read_bps_stats,omitempty"`
	WriteBPSStats           BenchmarkStats                   `json:"write_bps_stats,omitempty"`
	Operations              map[string]BenchmarkPhaseSummary `json:"operations,omitempty"`
	EventOperationSummaries map[string]BenchmarkPhaseSummary `json:"event_operation_summaries,omitempty"`
	VFS                     *BenchmarkVFSSummary             `json:"vfs,omitempty"`
}

type BenchmarkVFSSummary struct {
	PendingMax            int     `json:"pending_max"`
	PendingFinal          int     `json:"pending_final"`
	UploadMax             int     `json:"upload_max"`
	UploadFinal           int     `json:"upload_final"`
	DeleteTimerMax        int     `json:"delete_timer_max"`
	DeleteTimerFinal      int     `json:"delete_timer_final"`
	PendingDrainMS        int64   `json:"pending_drain_ms,omitempty"`
	CleanupDrainMS        int64   `json:"cleanup_drain_ms,omitempty"`
	CacheHits             int64   `json:"cache_hits,omitempty"`
	CacheMisses           int64   `json:"cache_misses,omitempty"`
	CacheHitRatio         float64 `json:"cache_hit_ratio,omitempty"`
	CacheErrors           int     `json:"cache_errors,omitempty"`
	ReadCacheFiles        int     `json:"read_cache_files,omitempty"`
	ReadCacheBytes        int64   `json:"read_cache_bytes,omitempty"`
	StagingOrphans        int     `json:"staging_orphans,omitempty"`
	StagingSizeMismatches int     `json:"staging_size_mismatches,omitempty"`
	ChunkLoads            int     `json:"chunk_loads,omitempty"`
	WindowLoads           int     `json:"window_loads,omitempty"`
	Prefetches            int     `json:"prefetches,omitempty"`
}

type BenchmarkPhaseSummary struct {
	Count           int            `json:"count"`
	OK              int            `json:"ok,omitempty"`
	Errors          int            `json:"errors,omitempty"`
	DurationStats   BenchmarkStats `json:"duration_ms_stats,omitempty"`
	Bytes           int64          `json:"bytes,omitempty"`
	ThroughputStats BenchmarkStats `json:"throughput_stats,omitempty"`
	ErrorCategories map[string]int `json:"error_categories,omitempty"`
}

type BenchmarkStats struct {
	Count  int     `json:"count,omitempty"`
	Min    int64   `json:"min,omitempty"`
	Median int64   `json:"median,omitempty"`
	P95    int64   `json:"p95,omitempty"`
	Max    int64   `json:"max,omitempty"`
	CV     float64 `json:"cv,omitempty"`
}

type BenchmarkAssessment struct {
	Status                string   `json:"status"`
	Confidence            string   `json:"confidence"`
	PerformanceComparable bool     `json:"performance_comparable"`
	Reasons               []string `json:"reasons,omitempty"`
}

type BenchmarkEnvironment struct {
	NetworkProbe *BenchmarkNetworkProbe `json:"network_probe,omitempty"`
}

type BenchmarkNetworkProbe struct {
	Status          string               `json:"status,omitempty"`
	Started         time.Time            `json:"started_at,omitempty"`
	Finished        time.Time            `json:"finished_at,omitempty"`
	Duration        string               `json:"duration,omitempty"`
	DurationMS      int64                `json:"duration_ms,omitempty"`
	Steps           []BenchmarkProbeStep `json:"steps,omitempty"`
	APILatency      BenchmarkStats       `json:"api_latency_ms,omitempty"`
	EventCount      int                  `json:"event_count"`
	RetryCount      int                  `json:"retry_count"`
	ErrorCount      int                  `json:"error_count"`
	EventOperations map[string]int       `json:"event_operations,omitempty"`
	Events          []drive.MetricEvent  `json:"events,omitempty"`
}

type BenchmarkProbeStep struct {
	Operation     string         `json:"operation"`
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	ErrorCategory string         `json:"error_category,omitempty"`
	Duration      string         `json:"duration,omitempty"`
	DurationMS    int64          `json:"duration_ms,omitempty"`
	Actual        map[string]any `json:"actual,omitempty"`
}

type BenchmarkCase struct {
	SampleIndex   int            `json:"sample_index,omitempty"`
	Operation     string         `json:"operation"`
	Name          string         `json:"name,omitempty"`
	OK            bool           `json:"ok"`
	Error         string         `json:"error,omitempty"`
	ErrorCategory string         `json:"error_category,omitempty"`
	Duration      string         `json:"duration,omitempty"`
	DurationMS    int64          `json:"duration_ms,omitempty"`
	Bytes         int64          `json:"bytes,omitempty"`
	Throughput    int64          `json:"throughput,omitempty"`
	Input         map[string]any `json:"input,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Actual        map[string]any `json:"actual,omitempty"`
}

func NewCRUDBenchmarkReport(result CRUDTestResult) BenchmarkReport {
	return NewCRUDBenchmarkReportSamples([]CRUDTestResult{result})
}

func NewCRUDBenchmarkReportSamples(results []CRUDTestResult) BenchmarkReport {
	return NewCRUDBenchmarkReportSamplesWithEnvironment(results, BenchmarkEnvironment{})
}

func NewCRUDBenchmarkReportSamplesWithEnvironment(results []CRUDTestResult, env BenchmarkEnvironment) BenchmarkReport {
	if len(results) == 0 {
		report := BenchmarkReport{
			SchemaVersion: BenchmarkSchemaVersion,
			Kind:          "driver_crud_benchmark",
			Environment:   env,
		}
		report.Assessment = benchmarkAssessment(false, report.Summary, env.NetworkProbe)
		return report
	}
	first := results[0]
	last := results[len(results)-1]
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "driver_crud_benchmark",
		Mount:         first.Mount,
		Driver:        first.Driver,
		Pass:          true,
		Started:       first.Started,
		Finished:      last.Finished,
		Environment:   env,
		Raw:           append([]CRUDTestResult(nil), results...),
	}
	if report.Finished.After(report.Started) {
		duration := report.Finished.Sub(report.Started)
		report.Duration = duration.String()
		report.DurationMS = durationMillis(duration)
	}
	for i, result := range results {
		if !result.Pass {
			report.Pass = false
		}
		sampleReport := newSingleCRUDBenchmarkReport(result, i+1)
		report.Samples = append(report.Samples, BenchmarkSample{
			Index:      i + 1,
			Pass:       sampleReport.Pass,
			Started:    sampleReport.Started,
			Finished:   sampleReport.Finished,
			Duration:   sampleReport.Duration,
			DurationMS: sampleReport.DurationMS,
			Summary:    sampleReport.Summary,
			Assessment: sampleReport.Assessment,
		})
		report.Cases = append(report.Cases, sampleReport.Cases...)
		report.Events = append(report.Events, sampleReport.Events...)
	}
	if len(results) == 1 {
		report.Samples = nil
		report.Duration = first.Duration
		report.DurationMS = first.DurationMS
	}
	report.Summary = benchmarkSummaryFromSamples(results, report.Cases)
	report.Assessment = benchmarkAssessment(report.Pass, report.Summary, report.Environment.NetworkProbe)
	return report
}

func NewFSBenchmarkReportSamplesWithEnvironment(results []FSTestResult, env BenchmarkEnvironment) BenchmarkReport {
	if len(results) == 0 {
		report := BenchmarkReport{
			SchemaVersion: BenchmarkSchemaVersion,
			Kind:          "vfs_fs_benchmark",
			Environment:   env,
		}
		report.Assessment = benchmarkAssessment(false, report.Summary, env.NetworkProbe)
		return report
	}
	first := results[0]
	last := results[len(results)-1]
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "vfs_fs_benchmark",
		Mount:         first.Mount,
		Pass:          true,
		Started:       first.Started,
		Finished:      last.Finished,
		Environment:   env,
		Raw:           append([]FSTestResult(nil), results...),
	}
	if report.Finished.After(report.Started) {
		duration := report.Finished.Sub(report.Started)
		report.Duration = duration.String()
		report.DurationMS = durationMillis(duration)
	}
	for i, result := range results {
		if !result.Pass {
			report.Pass = false
		}
		sampleReport := newSingleFSBenchmarkReport(result, i+1)
		report.Samples = append(report.Samples, BenchmarkSample{
			Index:      i + 1,
			Pass:       sampleReport.Pass,
			Started:    sampleReport.Started,
			Finished:   sampleReport.Finished,
			Duration:   sampleReport.Duration,
			DurationMS: sampleReport.DurationMS,
			Summary:    sampleReport.Summary,
			Assessment: sampleReport.Assessment,
		})
		report.Cases = append(report.Cases, sampleReport.Cases...)
	}
	if len(results) == 1 {
		report.Samples = nil
		report.Duration = first.Duration
		report.DurationMS = first.DurationMS
	}
	report.Summary = benchmarkSummaryFromFSSamples(results, report.Cases)
	report.Assessment = benchmarkAssessment(report.Pass, report.Summary, report.Environment.NetworkProbe)
	return report
}

func NewXferBenchmarkReportSamplesWithEnvironment(results []XferTestResult, env BenchmarkEnvironment) BenchmarkReport {
	if len(results) == 0 {
		report := BenchmarkReport{
			SchemaVersion: BenchmarkSchemaVersion,
			Kind:          "xfer_benchmark",
			Environment:   env,
		}
		report.Assessment = benchmarkAssessment(false, report.Summary, env.NetworkProbe)
		return report
	}
	first := results[0]
	last := results[len(results)-1]
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "xfer_benchmark",
		SourceMount:   first.SourceMount,
		DestMount:     first.DestMount,
		SourceDriver:  first.SourceType,
		DestDriver:    first.DestType,
		VFS:           first.VFS,
		Pass:          true,
		Started:       first.Started,
		Finished:      last.Finished,
		Environment:   env,
		Raw:           append([]XferTestResult(nil), results...),
	}
	if report.Finished.After(report.Started) {
		duration := report.Finished.Sub(report.Started)
		report.Duration = duration.String()
		report.DurationMS = durationMillis(duration)
	}
	for i, result := range results {
		if !result.Pass {
			report.Pass = false
		}
		sampleReport := newSingleXferBenchmarkReport(result, i+1)
		report.Samples = append(report.Samples, BenchmarkSample{
			Index:      i + 1,
			Pass:       sampleReport.Pass,
			Started:    sampleReport.Started,
			Finished:   sampleReport.Finished,
			Duration:   sampleReport.Duration,
			DurationMS: sampleReport.DurationMS,
			Summary:    sampleReport.Summary,
			Assessment: sampleReport.Assessment,
		})
		report.Cases = append(report.Cases, sampleReport.Cases...)
		report.Events = append(report.Events, sampleReport.Events...)
	}
	if len(results) == 1 {
		report.Samples = nil
		report.Duration = first.Metrics.WallTime
		report.DurationMS = first.Metrics.WallMS
	}
	report.Summary = benchmarkSummaryFromXferSamples(results, report.Cases)
	report.Assessment = benchmarkAssessment(report.Pass, report.Summary, report.Environment.NetworkProbe)
	return report
}

func newSingleFSBenchmarkReport(result FSTestResult, sampleIndex int) BenchmarkReport {
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "vfs_fs_benchmark",
		Mount:         result.Mount,
		Pass:          result.Pass,
		Started:       result.Started,
		Finished:      result.Finished,
		Duration:      result.Duration,
		DurationMS:    result.DurationMS,
		Raw:           []FSTestResult{result},
	}
	report.Cases = benchmarkCasesFromFS(result, sampleIndex)
	report.Summary = benchmarkSummaryFromFSTest(result, report.Cases)
	report.Assessment = benchmarkAssessment(result.Pass, report.Summary, nil)
	return report
}

func newSingleXferBenchmarkReport(result XferTestResult, sampleIndex int) BenchmarkReport {
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "xfer_benchmark",
		SourceMount:   result.SourceMount,
		DestMount:     result.DestMount,
		SourceDriver:  result.SourceType,
		DestDriver:    result.DestType,
		VFS:           result.VFS,
		Pass:          result.Pass,
		Started:       result.Started,
		Finished:      result.Finished,
		Duration:      result.Metrics.WallTime,
		DurationMS:    result.Metrics.WallMS,
		Events:        result.Timeline,
		Raw:           []XferTestResult{result},
	}
	report.Cases = benchmarkCasesFromXfer(result, sampleIndex)
	report.Summary = benchmarkSummaryFromXfer(result, report.Cases)
	report.Assessment = benchmarkAssessment(result.Pass, report.Summary, nil)
	return report
}

func newSingleCRUDBenchmarkReport(result CRUDTestResult, sampleIndex int) BenchmarkReport {
	report := BenchmarkReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "driver_crud_benchmark",
		Mount:         result.Mount,
		Driver:        result.Driver,
		Pass:          result.Pass,
		Started:       result.Started,
		Finished:      result.Finished,
		Duration:      result.Duration,
		DurationMS:    result.DurationMS,
		Events:        result.Metrics,
		Raw:           []CRUDTestResult{result},
	}
	report.Cases = benchmarkCasesFromCRUD(result, sampleIndex)
	report.Summary = benchmarkSummaryFromCRUD(result, report.Cases)
	report.Assessment = benchmarkAssessment(result.Pass, report.Summary, nil)
	return report
}

func CompareBenchmarkReports(base, current BenchmarkReport) BenchmarkCompareReport {
	report := BenchmarkCompareReport{
		SchemaVersion: BenchmarkSchemaVersion,
		Kind:          "benchmark_comparison",
		Pass:          true,
		Status:        "pass",
		Base:          benchmarkCompareInput(base),
		Current:       benchmarkCompareInput(current),
	}
	addDiff := func(metric, severity string, baseValue, currentValue any, message string) {
		report.Differences = append(report.Differences, BenchmarkDifference{
			Metric:   metric,
			Severity: severity,
			Base:     baseValue,
			Current:  currentValue,
			Message:  message,
		})
		if severity == "fail" {
			report.Pass = false
			report.Status = "fail"
		} else if severity == "warning" && report.Status == "pass" {
			report.Status = "warning"
		}
	}

	if base.Kind != current.Kind {
		addDiff("kind", "fail", base.Kind, current.Kind, "benchmark kind changed")
	}
	if base.Mount != current.Mount {
		addDiff("mount", "warning", base.Mount, current.Mount, "benchmark mount changed")
	}
	if base.Driver != current.Driver {
		addDiff("driver", "warning", base.Driver, current.Driver, "benchmark driver changed")
	}
	if base.SourceMount != current.SourceMount {
		addDiff("source_mount", "warning", base.SourceMount, current.SourceMount, "benchmark source mount changed")
	}
	if base.DestMount != current.DestMount {
		addDiff("dest_mount", "warning", base.DestMount, current.DestMount, "benchmark destination mount changed")
	}
	if base.SourceDriver != current.SourceDriver {
		addDiff("source_driver", "warning", base.SourceDriver, current.SourceDriver, "benchmark source driver changed")
	}
	if base.DestDriver != current.DestDriver {
		addDiff("dest_driver", "warning", base.DestDriver, current.DestDriver, "benchmark destination driver changed")
	}
	if base.VFS != current.VFS {
		addDiff("vfs", "warning", base.VFS, current.VFS, "benchmark VFS mode changed")
	}
	if current.Environment.NetworkProbe == nil || current.Environment.NetworkProbe.Status == "" {
		addDiff("environment.network_probe.status", "warning", "", "", "current benchmark has no network probe")
	} else if current.Environment.NetworkProbe.Status != "ok" {
		addDiff("environment.network_probe.status", "warning", "ok", current.Environment.NetworkProbe.Status, "current network probe is not ok")
	}
	if base.Pass && !current.Pass {
		addDiff("pass", "fail", base.Pass, current.Pass, "benchmark no longer passes")
	} else if !base.Pass && current.Pass {
		addDiff("pass", "info", base.Pass, current.Pass, "benchmark now passes")
	}

	compareIntIncrease(addDiff, "summary.error_count", base.Summary.ErrorCount, current.Summary.ErrorCount, "error count increased")
	compareIntIncrease(addDiff, "summary.retry_count", base.Summary.RetryCount, current.Summary.RetryCount, "retry count increased")
	compareIntIncrease(addDiff, "summary.cleanup_residuals", base.Summary.CleanupResiduals, current.Summary.CleanupResiduals, "cleanup residuals increased")
	compareIntIncrease(addDiff, "summary.failed_cases", base.Summary.FailedCases, current.Summary.FailedCases, "failed case count increased")
	if current.Summary.EventCount < base.Summary.EventCount {
		addDiff("summary.event_count", "warning", base.Summary.EventCount, current.Summary.EventCount, "metric event count decreased")
	}
	compareEventOperations(addDiff, base.Summary.EventOperations, current.Summary.EventOperations)
	compareVFSSummary(addDiff, base.Summary.VFS, current.Summary.VFS)
	compareBenchmarkPerformance(addDiff, base, current)
	return report
}

func benchmarkCompareInput(report BenchmarkReport) BenchmarkCompareInput {
	return BenchmarkCompareInput{
		Kind:         report.Kind,
		Mount:        report.Mount,
		Driver:       report.Driver,
		SourceMount:  report.SourceMount,
		DestMount:    report.DestMount,
		SourceDriver: report.SourceDriver,
		DestDriver:   report.DestDriver,
		VFS:          report.VFS,
		Pass:         report.Pass,
		DurationMS:   report.DurationMS,
	}
}

func compareIntIncrease(addDiff func(string, string, any, any, string), metric string, baseValue, currentValue int, message string) {
	if currentValue > baseValue {
		addDiff(metric, "warning", baseValue, currentValue, message)
	}
}

func compareEventOperations(addDiff func(string, string, any, any, string), base, current map[string]int) {
	for operation, baseCount := range base {
		currentCount := current[operation]
		if currentCount < baseCount {
			addDiff("summary.event_operations."+operation, "warning", baseCount, currentCount, "event operation count decreased")
		}
	}
	for operation, currentCount := range current {
		if _, ok := base[operation]; !ok {
			addDiff("summary.event_operations."+operation, "info", 0, currentCount, "new event operation observed")
		}
	}
}

func compareBenchmarkPerformance(addDiff func(string, string, any, any, string), base, current BenchmarkReport) {
	if !base.Assessment.PerformanceComparable || !current.Assessment.PerformanceComparable {
		addDiff("performance.comparable", "info", base.Assessment.PerformanceComparable, current.Assessment.PerformanceComparable, "performance comparison is inconclusive")
		return
	}
	compareLatencyRegression(addDiff, "summary.duration_ms_stats.median", base.Summary.DurationStats.Median, current.Summary.DurationStats.Median, 0.30)
	compareLatencyRegression(addDiff, "summary.duration_ms_stats.p95", base.Summary.DurationStats.P95, current.Summary.DurationStats.P95, 0.30)
	compareThroughputRegression(addDiff, "summary.read_bps_stats.median", base.Summary.ReadBPSStats.Median, current.Summary.ReadBPSStats.Median, 0.25)
	compareThroughputRegression(addDiff, "summary.write_bps_stats.median", base.Summary.WriteBPSStats.Median, current.Summary.WriteBPSStats.Median, 0.25)
	comparePhaseLatency(addDiff, "summary.operations", base.Summary.Operations, current.Summary.Operations)
	comparePhaseLatency(addDiff, "summary.event_operation_summaries", base.Summary.EventOperationSummaries, current.Summary.EventOperationSummaries)
	if base.Summary.VFS != nil && current.Summary.VFS != nil {
		compareLatencyRegression(addDiff, "summary.vfs.pending_drain_ms", base.Summary.VFS.PendingDrainMS, current.Summary.VFS.PendingDrainMS, 0.30)
		compareLatencyRegression(addDiff, "summary.vfs.cleanup_drain_ms", base.Summary.VFS.CleanupDrainMS, current.Summary.VFS.CleanupDrainMS, 0.30)
		compareRatioDrop(addDiff, "summary.vfs.cache_hit_ratio", base.Summary.VFS.CacheHitRatio, current.Summary.VFS.CacheHitRatio, 0.25)
	}
}

func compareLatencyRegression(addDiff func(string, string, any, any, string), metric string, baseValue, currentValue int64, threshold float64) {
	if baseValue <= 0 || currentValue <= 0 {
		return
	}
	ratio := float64(currentValue-baseValue) / float64(baseValue)
	if ratio > threshold {
		addDiff(metric, "warning", baseValue, currentValue, fmt.Sprintf("latency regressed by %.1f%%", ratio*100))
	}
}

func compareThroughputRegression(addDiff func(string, string, any, any, string), metric string, baseValue, currentValue int64, threshold float64) {
	if baseValue <= 0 || currentValue <= 0 {
		return
	}
	ratio := float64(baseValue-currentValue) / float64(baseValue)
	if ratio > threshold {
		addDiff(metric, "warning", baseValue, currentValue, fmt.Sprintf("throughput regressed by %.1f%%", ratio*100))
	}
}

func comparePhaseLatency(addDiff func(string, string, any, any, string), prefix string, base, current map[string]BenchmarkPhaseSummary) {
	for operation, baseSummary := range base {
		currentSummary, ok := current[operation]
		if !ok {
			continue
		}
		compareLatencyRegression(addDiff, prefix+"."+operation+".duration_ms_stats.median", baseSummary.DurationStats.Median, currentSummary.DurationStats.Median, 0.30)
		compareLatencyRegression(addDiff, prefix+"."+operation+".duration_ms_stats.p95", baseSummary.DurationStats.P95, currentSummary.DurationStats.P95, 0.30)
	}
}

func compareVFSSummary(addDiff func(string, string, any, any, string), base, current *BenchmarkVFSSummary) {
	if current == nil {
		return
	}
	if current.PendingFinal > 0 {
		addDiff("summary.vfs.pending_final", "fail", 0, current.PendingFinal, "VFS pending queue did not drain")
	}
	if current.UploadFinal > 0 {
		addDiff("summary.vfs.upload_final", "fail", 0, current.UploadFinal, "VFS uploads did not drain")
	}
	if current.DeleteTimerFinal > 0 {
		addDiff("summary.vfs.delete_timer_final", "fail", 0, current.DeleteTimerFinal, "VFS delete timers did not drain")
	}
	if current.StagingOrphans > 0 {
		addDiff("summary.vfs.staging_orphans", "warning", 0, current.StagingOrphans, "staging orphans remain after benchmark")
	}
	if current.StagingSizeMismatches > 0 {
		addDiff("summary.vfs.staging_size_mismatches", "warning", 0, current.StagingSizeMismatches, "staging size mismatches remain after benchmark")
	}
	if current.CacheErrors > 0 {
		addDiff("summary.vfs.cache_errors", "warning", 0, current.CacheErrors, "read cache reported errors")
	}
	if base != nil {
		compareIntIncrease(addDiff, "summary.vfs.pending_max", base.PendingMax, current.PendingMax, "VFS pending max increased")
		compareIntIncrease(addDiff, "summary.vfs.upload_max", base.UploadMax, current.UploadMax, "VFS upload max increased")
		compareIntIncrease(addDiff, "summary.vfs.delete_timer_max", base.DeleteTimerMax, current.DeleteTimerMax, "VFS delete timer max increased")
	}
}

func compareRatioDrop(addDiff func(string, string, any, any, string), metric string, baseValue, currentValue float64, threshold float64) {
	if baseValue <= 0 || currentValue < 0 {
		return
	}
	ratio := (baseValue - currentValue) / baseValue
	if ratio > threshold {
		addDiff(metric, "warning", baseValue, currentValue, fmt.Sprintf("ratio regressed by %.1f%%", ratio*100))
	}
}

func benchmarkCasesFromCRUD(result CRUDTestResult, sampleIndex int) []BenchmarkCase {
	cases := make([]BenchmarkCase, 0, len(result.Steps)+len(result.Cleanup))
	for _, step := range result.Steps {
		cases = append(cases, BenchmarkCase{
			SampleIndex:   sampleIndex,
			Operation:     step.Operation,
			Name:          step.Name,
			OK:            step.OK,
			Error:         step.Error,
			ErrorCategory: step.ErrorCategory,
			Duration:      step.Duration,
			DurationMS:    step.DurationMS,
			Bytes:         benchmarkStepBytes(step),
			Throughput:    benchmarkThroughput(benchmarkStepBytes(step), step.DurationMS),
			Input:         step.Input,
			Expected:      step.Expected,
			Actual:        step.Actual,
		})
	}
	for _, cleanup := range result.Cleanup {
		cases = append(cases, BenchmarkCase{
			SampleIndex:   sampleIndex,
			Operation:     "cleanup_" + cleanup.Operation,
			Name:          cleanup.Name,
			OK:            cleanup.OK,
			Error:         cleanup.Error,
			ErrorCategory: cleanup.ErrorCategory,
			Duration:      cleanup.Duration,
			DurationMS:    cleanup.DurationMS,
		})
	}
	return cases
}

func benchmarkCasesFromFS(result FSTestResult, sampleIndex int) []BenchmarkCase {
	cases := make([]BenchmarkCase, 0, len(result.Steps))
	for _, step := range result.Steps {
		cases = append(cases, BenchmarkCase{
			SampleIndex:   sampleIndex,
			Operation:     step.Operation,
			OK:            step.OK,
			Error:         step.Error,
			ErrorCategory: step.ErrorCategory,
			Duration:      step.Duration,
			DurationMS:    step.DurationMS,
			Bytes:         benchmarkFSStepBytes(step),
			Throughput:    benchmarkThroughput(benchmarkFSStepBytes(step), step.DurationMS),
			Input:         step.Input,
			Expected:      step.Expected,
			Actual:        step.Actual,
		})
	}
	return cases
}

func benchmarkCasesFromXfer(result XferTestResult, sampleIndex int) []BenchmarkCase {
	cases := make([]BenchmarkCase, 0, len(result.Steps))
	for _, step := range result.Steps {
		cases = append(cases, BenchmarkCase{
			SampleIndex:   sampleIndex,
			Operation:     step.Phase,
			OK:            step.OK,
			Error:         step.Error,
			ErrorCategory: step.ErrorCategory,
			Duration:      step.Duration,
			DurationMS:    step.DurationMS,
			Bytes:         step.Bytes,
			Throughput:    benchmarkThroughput(step.Bytes, step.DurationMS),
		})
	}
	return cases
}

func benchmarkSummaryFromSamples(results []CRUDTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		SampleCount:     len(results),
		TotalCases:      len(cases),
		EventOperations: map[string]int{},
	}
	var durations []int64
	var readBPS []int64
	var writeBPS []int64
	for _, result := range results {
		if result.Pass {
			summary.PassedSamples++
		} else {
			summary.FailedSamples++
		}
		single := newSingleCRUDBenchmarkReport(result, 0).Summary
		summary.PassedCases += single.PassedCases
		summary.FailedCases += single.FailedCases
		summary.ErrorCount += single.ErrorCount
		summary.RetryCount += single.RetryCount
		summary.EventCount += single.EventCount
		summary.CleanupResiduals += single.CleanupResiduals
		summary.BytesRead += single.BytesRead
		summary.BytesWritten += single.BytesWritten
		if single.MaxDurationMS > summary.MaxDurationMS {
			summary.MaxDurationMS = single.MaxDurationMS
		}
		if result.DurationMS > 0 {
			durations = append(durations, result.DurationMS)
		}
		if single.ReadBPS > 0 {
			readBPS = append(readBPS, single.ReadBPS)
		}
		if single.WriteBPS > 0 {
			writeBPS = append(writeBPS, single.WriteBPS)
		}
		for operation, count := range single.EventOperations {
			summary.EventOperations[operation] += count
		}
	}
	if len(summary.EventOperations) == 0 {
		summary.EventOperations = nil
	}
	summary.P95DurationMS = percentileDuration(benchmarkCaseDurations(cases), 95)
	summary.ReadBPS = benchmarkThroughput(summary.BytesRead, sumResultDurationMS(results))
	summary.WriteBPS = benchmarkThroughput(summary.BytesWritten, sumResultDurationMS(results))
	summary.DurationStats = benchmarkStats(durations)
	summary.ReadBPSStats = benchmarkStats(readBPS)
	summary.WriteBPSStats = benchmarkStats(writeBPS)
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.EventOperationSummaries = benchmarkEventOperationSummaries(results)
	return summary
}

func benchmarkSummaryFromXferSamples(results []XferTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		SampleCount:     len(results),
		TotalCases:      len(cases),
		EventOperations: map[string]int{},
	}
	var durations []int64
	var readBPS []int64
	var writeBPS []int64
	for _, result := range results {
		if result.Pass {
			summary.PassedSamples++
		} else {
			summary.FailedSamples++
		}
		single := newSingleXferBenchmarkReport(result, 0).Summary
		summary.PassedCases += single.PassedCases
		summary.FailedCases += single.FailedCases
		summary.ErrorCount += single.ErrorCount
		summary.RetryCount += single.RetryCount
		summary.EventCount += single.EventCount
		summary.BytesRead += single.BytesRead
		summary.BytesWritten += single.BytesWritten
		if single.MaxDurationMS > summary.MaxDurationMS {
			summary.MaxDurationMS = single.MaxDurationMS
		}
		if result.Metrics.WallMS > 0 {
			durations = append(durations, result.Metrics.WallMS)
		}
		if single.ReadBPS > 0 {
			readBPS = append(readBPS, single.ReadBPS)
		}
		if single.WriteBPS > 0 {
			writeBPS = append(writeBPS, single.WriteBPS)
		}
		for operation, count := range single.EventOperations {
			summary.EventOperations[operation] += count
		}
	}
	if len(summary.EventOperations) == 0 {
		summary.EventOperations = nil
	}
	summary.P95DurationMS = percentileDuration(benchmarkCaseDurations(cases), 95)
	summary.DurationStats = benchmarkStats(durations)
	summary.ReadBPSStats = benchmarkStats(readBPS)
	summary.WriteBPSStats = benchmarkStats(writeBPS)
	summary.ReadBPS = summary.ReadBPSStats.Median
	summary.WriteBPS = summary.WriteBPSStats.Median
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.EventOperationSummaries = benchmarkXferEventOperationSummaries(results)
	return summary
}

func benchmarkSummaryFromFSSamples(results []FSTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		SampleCount: len(results),
		TotalCases:  len(cases),
	}
	var durations []int64
	var readBPS []int64
	var writeBPS []int64
	for _, result := range results {
		if result.Pass {
			summary.PassedSamples++
		} else {
			summary.FailedSamples++
		}
		single := newSingleFSBenchmarkReport(result, 0).Summary
		summary.PassedCases += single.PassedCases
		summary.FailedCases += single.FailedCases
		summary.ErrorCount += single.ErrorCount
		summary.BytesRead += single.BytesRead
		summary.BytesWritten += single.BytesWritten
		if single.MaxDurationMS > summary.MaxDurationMS {
			summary.MaxDurationMS = single.MaxDurationMS
		}
		if result.DurationMS > 0 {
			durations = append(durations, result.DurationMS)
		}
		if single.ReadBPS > 0 {
			readBPS = append(readBPS, single.ReadBPS)
		}
		if single.WriteBPS > 0 {
			writeBPS = append(writeBPS, single.WriteBPS)
		}
	}
	summary.P95DurationMS = percentileDuration(benchmarkCaseDurations(cases), 95)
	summary.ReadBPS = benchmarkThroughput(summary.BytesRead, sumFSTestDurationMS(results))
	summary.WriteBPS = benchmarkThroughput(summary.BytesWritten, sumFSTestDurationMS(results))
	summary.DurationStats = benchmarkStats(durations)
	summary.ReadBPSStats = benchmarkStats(readBPS)
	summary.WriteBPSStats = benchmarkStats(writeBPS)
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.VFS = benchmarkVFSSummary(results)
	return summary
}

func benchmarkSummaryFromCRUD(result CRUDTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		TotalCases:       len(cases),
		EventCount:       len(result.Metrics),
		EventOperations:  map[string]int{},
		CleanupResiduals: len(result.Residual),
	}
	var durations []int64
	for _, item := range cases {
		if item.OK {
			summary.PassedCases++
		} else {
			summary.FailedCases++
		}
		if item.Error != "" {
			summary.ErrorCount++
		}
		if item.DurationMS > 0 {
			durations = append(durations, item.DurationMS)
			if item.DurationMS > summary.MaxDurationMS {
				summary.MaxDurationMS = item.DurationMS
			}
		}
		switch item.Operation {
		case "read":
			summary.BytesRead += item.Bytes
		case "put":
			summary.BytesWritten += item.Bytes
		}
	}
	for _, event := range result.Metrics {
		if event.Operation != "" {
			summary.EventOperations[event.Operation]++
		}
		summary.RetryCount += event.RetryCount
		if event.Attempts > 1 {
			summary.RetryCount += event.Attempts - 1
		}
	}
	if len(summary.EventOperations) == 0 {
		summary.EventOperations = nil
	}
	summary.P95DurationMS = percentileDuration(durations, 95)
	summary.ReadBPS = benchmarkThroughput(summary.BytesRead, result.DurationMS)
	summary.WriteBPS = benchmarkThroughput(summary.BytesWritten, result.DurationMS)
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.EventOperationSummaries = benchmarkEventOperationSummaries([]CRUDTestResult{result})
	return summary
}

func benchmarkSummaryFromFSTest(result FSTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		TotalCases: len(cases),
	}
	var durations []int64
	for _, item := range cases {
		if item.OK {
			summary.PassedCases++
		} else {
			summary.FailedCases++
		}
		if item.Error != "" {
			summary.ErrorCount++
		}
		if item.DurationMS > 0 {
			durations = append(durations, item.DurationMS)
			if item.DurationMS > summary.MaxDurationMS {
				summary.MaxDurationMS = item.DurationMS
			}
		}
		switch item.Operation {
		case "read":
			summary.BytesRead += item.Bytes
		case "write":
			summary.BytesWritten += item.Bytes
		}
	}
	summary.P95DurationMS = percentileDuration(durations, 95)
	summary.ReadBPS = benchmarkThroughput(summary.BytesRead, result.DurationMS)
	summary.WriteBPS = benchmarkThroughput(summary.BytesWritten, result.DurationMS)
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.VFS = benchmarkVFSSummary([]FSTestResult{result})
	return summary
}

func benchmarkSummaryFromXfer(result XferTestResult, cases []BenchmarkCase) BenchmarkSummary {
	summary := BenchmarkSummary{
		TotalCases:      len(cases),
		EventCount:      len(result.Timeline),
		EventOperations: map[string]int{},
	}
	var durations []int64
	for _, item := range cases {
		if item.OK {
			summary.PassedCases++
		} else {
			summary.FailedCases++
		}
		if item.Error != "" {
			summary.ErrorCount++
		}
		if item.DurationMS > 0 {
			durations = append(durations, item.DurationMS)
			if item.DurationMS > summary.MaxDurationMS {
				summary.MaxDurationMS = item.DurationMS
			}
		}
		switch item.Operation {
		case "read_source":
			summary.BytesRead += item.Bytes
		case "write_source", "staging_write_source", "write_dest", "staging_write_dest":
			summary.BytesWritten += item.Bytes
		}
	}
	for _, event := range result.Timeline {
		if event.Operation != "" {
			summary.EventOperations[event.Operation]++
		}
		summary.RetryCount += event.RetryCount
		if event.Attempts > 1 {
			summary.RetryCount += event.Attempts - 1
		}
	}
	if len(summary.EventOperations) == 0 {
		summary.EventOperations = nil
	}
	summary.P95DurationMS = percentileDuration(durations, 95)
	summary.ReadBPS = result.Metrics.ReadThroughput
	summary.WriteBPS = result.Metrics.WriteThroughput
	summary.Operations = benchmarkOperationSummaries(cases)
	summary.EventOperationSummaries = benchmarkXferEventOperationSummaries([]XferTestResult{result})
	return summary
}

func RunBenchmarkNetworkProbe(ctx context.Context, d drive.Driver) BenchmarkNetworkProbe {
	probe := BenchmarkNetworkProbe{
		Status:          "ok",
		Started:         time.Now(),
		EventOperations: map[string]int{},
	}
	addStep := func(operation string, start time.Time, err error, actual map[string]any) {
		duration := time.Since(start)
		step := BenchmarkProbeStep{
			Operation:  operation,
			OK:         err == nil,
			Duration:   duration.String(),
			DurationMS: durationMillis(duration),
			Actual:     actual,
		}
		if err != nil {
			step.Error = err.Error()
			step.ErrorCategory = drive.ErrorCategory(err)
			probe.ErrorCount++
		}
		probe.Steps = append(probe.Steps, step)
	}

	start := time.Now()
	snapshot, err := d.DebugSnapshot(ctx)
	addStep("debug_snapshot", start, err, map[string]any{"driver": snapshot.Driver, "health": snapshot.Health})

	rootID := "root"
	if drive.HasCapability(d, drive.CapabilityPathResolver) {
		start = time.Now()
		resolved, resolveErr := d.ResolvePath(ctx, "/")
		if resolved != "" {
			rootID = resolved
		}
		addStep("resolve_root", start, resolveErr, map[string]any{"root_id": resolved})
		if resolveErr != nil {
			err = resolveErr
		}
	}

	start = time.Now()
	entries, usedRootID, listErr := listAuthRoot(ctx, d, rootID, !drive.HasCapability(d, drive.CapabilityPathResolver))
	addStep("list_root", start, listErr, map[string]any{"parent_id": usedRootID, "entry_count": len(entries)})
	if listErr != nil {
		err = listErr
	}

	probe.Finished = time.Now()
	duration := probe.Finished.Sub(probe.Started)
	probe.Duration = duration.String()
	probe.DurationMS = durationMillis(duration)
	probe.APILatency = benchmarkStats(probeStepDurations(probe.Steps))
	if metrics, metricsErr := d.Metrics(ctx, probe.Started); metricsErr == nil {
		probe.Events = metrics
		probe.EventCount = len(metrics)
		for _, event := range metrics {
			if event.Operation != "" {
				probe.EventOperations[event.Operation]++
			}
			if event.Error != "" {
				probe.ErrorCount++
			}
			probe.RetryCount += event.RetryCount
			if event.Attempts > 1 {
				probe.RetryCount += event.Attempts - 1
			}
		}
	}
	if len(probe.EventOperations) == 0 {
		probe.EventOperations = nil
	}
	switch {
	case err != nil:
		probe.Status = "degraded"
	case probe.APILatency.Count >= 3 && probe.APILatency.CV > 0.35:
		probe.Status = "unstable"
	case probe.RetryCount > 0:
		probe.Status = "unstable"
	default:
		probe.Status = "ok"
	}
	return probe
}

func benchmarkAssessment(pass bool, summary BenchmarkSummary, probe *BenchmarkNetworkProbe) BenchmarkAssessment {
	assessment := BenchmarkAssessment{
		Status:                "pass",
		Confidence:            "medium",
		PerformanceComparable: false,
	}
	if !pass {
		assessment.Status = "fail"
		assessment.Confidence = "high"
	}
	if probe == nil || probe.Status == "" {
		assessment.Reasons = append(assessment.Reasons, "network_probe_not_available")
	} else if probe.Status != "ok" {
		assessment.Reasons = append(assessment.Reasons, "network_probe_"+probe.Status)
	}
	if summary.SampleCount < 3 {
		assessment.Reasons = append(assessment.Reasons, "sample_count_below_3")
	} else if pass && summary.DurationStats.CV > 0.35 {
		assessment.Status = "inconclusive"
		assessment.Confidence = "low"
		assessment.Reasons = append(assessment.Reasons, "duration_cv_high")
	} else if pass && probe != nil && probe.Status == "ok" {
		assessment.PerformanceComparable = true
	}
	if summary.EventCount == 0 {
		assessment.Reasons = append(assessment.Reasons, "no_metric_events")
	}
	return assessment
}

func benchmarkOperationSummaries(cases []BenchmarkCase) map[string]BenchmarkPhaseSummary {
	builders := map[string]*benchmarkPhaseBuilder{}
	for _, item := range cases {
		if item.Operation == "" {
			continue
		}
		builder := builders[item.Operation]
		if builder == nil {
			builder = &benchmarkPhaseBuilder{errors: map[string]int{}}
			builders[item.Operation] = builder
		}
		builder.addCase(item)
	}
	return benchmarkPhaseSummaries(builders)
}

func benchmarkEventOperationSummaries(results []CRUDTestResult) map[string]BenchmarkPhaseSummary {
	builders := map[string]*benchmarkPhaseBuilder{}
	for _, result := range results {
		for _, event := range result.Metrics {
			if event.Operation == "" {
				continue
			}
			builder := builders[event.Operation]
			if builder == nil {
				builder = &benchmarkPhaseBuilder{errors: map[string]int{}}
				builders[event.Operation] = builder
			}
			builder.addEvent(event)
		}
	}
	return benchmarkPhaseSummaries(builders)
}

func benchmarkXferEventOperationSummaries(results []XferTestResult) map[string]BenchmarkPhaseSummary {
	builders := map[string]*benchmarkPhaseBuilder{}
	for _, result := range results {
		for _, event := range result.Timeline {
			if event.Operation == "" {
				continue
			}
			builder := builders[event.Operation]
			if builder == nil {
				builder = &benchmarkPhaseBuilder{errors: map[string]int{}}
				builders[event.Operation] = builder
			}
			builder.addEvent(event)
		}
	}
	return benchmarkPhaseSummaries(builders)
}

func benchmarkVFSSummary(results []FSTestResult) *BenchmarkVFSSummary {
	if len(results) == 0 {
		return nil
	}
	summary := &BenchmarkVFSSummary{}
	for _, result := range results {
		for _, sample := range result.PendingTimeline {
			if sample.PendingCount > summary.PendingMax {
				summary.PendingMax = sample.PendingCount
			}
			if sample.UploadCount > summary.UploadMax {
				summary.UploadMax = sample.UploadCount
			}
			if sample.DeleteTimers > summary.DeleteTimerMax {
				summary.DeleteTimerMax = sample.DeleteTimers
			}
		}
		for _, step := range result.Steps {
			switch step.Operation {
			case "wait_upload":
				if step.DurationMS > summary.PendingDrainMS {
					summary.PendingDrainMS = step.DurationMS
				}
			case "wait_cleanup":
				if step.DurationMS > summary.CleanupDrainMS {
					summary.CleanupDrainMS = step.DurationMS
				}
			}
		}
		if result.FinalState == nil {
			continue
		}
		state := result.FinalState
		summary.PendingFinal = state.PendingCount
		summary.UploadFinal = state.UploadCount
		summary.DeleteTimerFinal = state.DeleteTimers
		summary.CacheHits = state.CacheHits
		summary.CacheMisses = state.CacheMisses
		summary.CacheHitRatio = state.CacheHitRatio
		summary.CacheErrors = state.CacheErrors
		summary.ReadCacheFiles = state.ReadCacheFiles
		summary.ReadCacheBytes = state.ReadCacheBytes
		summary.StagingOrphans = state.StagingOrphans
		summary.StagingSizeMismatches = state.StagingSizeMismatches
		summary.ChunkLoads = state.ChunkLoads
		summary.WindowLoads = state.WindowLoads
		summary.Prefetches = state.Prefetches
	}
	return summary
}

type benchmarkPhaseBuilder struct {
	count       int
	ok          int
	errors      map[string]int
	durations   []int64
	bytes       int64
	throughputs []int64
}

func (b *benchmarkPhaseBuilder) addCase(item BenchmarkCase) {
	b.count++
	if item.OK {
		b.ok++
	}
	if item.ErrorCategory != "" {
		b.errors[item.ErrorCategory]++
	}
	if item.DurationMS > 0 {
		b.durations = append(b.durations, item.DurationMS)
	}
	if item.Bytes > 0 {
		b.bytes += item.Bytes
	}
	if item.Throughput > 0 {
		b.throughputs = append(b.throughputs, item.Throughput)
	}
}

func (b *benchmarkPhaseBuilder) addEvent(event drive.MetricEvent) {
	b.count++
	if event.OK {
		b.ok++
	}
	if event.ErrorCategory != "" {
		b.errors[event.ErrorCategory]++
	}
	if event.DurationMS > 0 {
		b.durations = append(b.durations, event.DurationMS)
	}
	if event.Bytes > 0 {
		b.bytes += event.Bytes
	}
	if event.Throughput > 0 {
		b.throughputs = append(b.throughputs, event.Throughput)
	}
}

func benchmarkPhaseSummaries(builders map[string]*benchmarkPhaseBuilder) map[string]BenchmarkPhaseSummary {
	if len(builders) == 0 {
		return nil
	}
	out := make(map[string]BenchmarkPhaseSummary, len(builders))
	for operation, builder := range builders {
		summary := BenchmarkPhaseSummary{
			Count:           builder.count,
			OK:              builder.ok,
			Errors:          builder.count - builder.ok,
			DurationStats:   benchmarkStats(builder.durations),
			Bytes:           builder.bytes,
			ThroughputStats: benchmarkStats(builder.throughputs),
			ErrorCategories: builder.errors,
		}
		if len(summary.ErrorCategories) == 0 {
			summary.ErrorCategories = nil
		}
		out[operation] = summary
	}
	return out
}

func benchmarkStepBytes(step CRUDTestStep) int64 {
	if step.Input == nil {
		return 0
	}
	value, ok := step.Input["bytes"]
	if !ok {
		return 0
	}
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func benchmarkFSStepBytes(step FSTestStep) int64 {
	if step.Input != nil {
		if bytes := benchmarkAnyInt64(step.Input["bytes"]); bytes > 0 {
			return bytes
		}
	}
	if step.Actual != nil {
		if bytes := benchmarkAnyInt64(step.Actual["bytes"]); bytes > 0 {
			return bytes
		}
	}
	return 0
}

func benchmarkAnyInt64(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func benchmarkThroughput(bytes, durationMS int64) int64 {
	if bytes <= 0 || durationMS <= 0 {
		return 0
	}
	return bytes * 1000 / durationMS
}

func benchmarkCaseDurations(cases []BenchmarkCase) []int64 {
	values := make([]int64, 0, len(cases))
	for _, item := range cases {
		if item.DurationMS > 0 {
			values = append(values, item.DurationMS)
		}
	}
	return values
}

func probeStepDurations(steps []BenchmarkProbeStep) []int64 {
	values := make([]int64, 0, len(steps))
	for _, step := range steps {
		if step.DurationMS > 0 {
			values = append(values, step.DurationMS)
		}
	}
	return values
}

func sumResultDurationMS(results []CRUDTestResult) int64 {
	var total int64
	for _, result := range results {
		total += result.DurationMS
	}
	return total
}

func sumFSTestDurationMS(results []FSTestResult) int64 {
	var total int64
	for _, result := range results {
		total += result.DurationMS
	}
	return total
}

func sumXferDurationMS(results []XferTestResult) int64 {
	var total int64
	for _, result := range results {
		total += result.Metrics.WallMS
	}
	return total
}

func benchmarkStats(values []int64) BenchmarkStats {
	if len(values) == 0 {
		return BenchmarkStats{}
	}
	sorted := sortedDurations(values)
	stats := BenchmarkStats{
		Count:  len(sorted),
		Min:    sorted[0],
		Median: percentileDuration(sorted, 50),
		P95:    percentileDuration(sorted, 95),
		Max:    sorted[len(sorted)-1],
	}
	var sum int64
	for _, value := range sorted {
		sum += value
	}
	mean := float64(sum) / float64(len(sorted))
	if mean <= 0 {
		return stats
	}
	var variance float64
	for _, value := range sorted {
		delta := float64(value) - mean
		variance += delta * delta
	}
	stats.CV = math.Sqrt(variance/float64(len(sorted))) / mean
	return stats
}

func percentileDuration(values []int64, percentile int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := sortedDurations(values)
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 100 {
		return sorted[len(sorted)-1]
	}
	index := (len(sorted)*percentile + 99) / 100
	if index <= 0 {
		index = 1
	}
	return sorted[index-1]
}

func sortedDurations(values []int64) []int64 {
	sorted := append([]int64(nil), values...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return sorted
}
