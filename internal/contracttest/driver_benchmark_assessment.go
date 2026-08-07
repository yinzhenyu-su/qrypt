package contracttest

import (
	"context"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

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
			DurationMS: DurationMillis(duration),
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
	probe.DurationMS = DurationMillis(duration)
	probe.APILatency = ComputeStats(ProbeStepDurations(probe.Steps))
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
