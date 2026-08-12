package contracttest

import "fmt"

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
