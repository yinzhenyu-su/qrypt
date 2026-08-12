package contracttest

import (
	"math"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
	summary.DurationStats = ComputeStats(durations)
	summary.ReadBPSStats = ComputeStats(readBPS)
	summary.WriteBPSStats = ComputeStats(writeBPS)
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
	summary.DurationStats = ComputeStats(durations)
	summary.ReadBPSStats = ComputeStats(readBPS)
	summary.WriteBPSStats = ComputeStats(writeBPS)
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
	summary.DurationStats = ComputeStats(durations)
	summary.ReadBPSStats = ComputeStats(readBPS)
	summary.WriteBPSStats = ComputeStats(writeBPS)
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
			DurationStats:   ComputeStats(builder.durations),
			Bytes:           builder.bytes,
			ThroughputStats: ComputeStats(builder.throughputs),
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

func ProbeStepDurations(steps []BenchmarkProbeStep) []int64 {
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

func ComputeStats(values []int64) BenchmarkStats {
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
