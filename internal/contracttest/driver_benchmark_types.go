package contracttest

import (
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
