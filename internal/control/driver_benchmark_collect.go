package control

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
