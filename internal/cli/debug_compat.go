package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	clidebug "github.com/yinzhenyu/qrypt/internal/cli/debug"
	"github.com/yinzhenyu/qrypt/pkg/contracttest"
)

const debugAIReportSchemaVersion = clidebug.DebugAIReportSchemaVersion

type debugSocketContextKey = clidebug.DebugSocketContextKey
type debugAIReport = clidebug.DebugAIReport
type debugAIInspect = clidebug.DebugAIInspect
type debugAIDiagnostic = clidebug.DebugAIDiagnostic
type debugAIError = clidebug.DebugAIError
type debugAIWatchReport = clidebug.DebugAIWatchReport
type debugAIWatchSample = clidebug.DebugAIWatchSample
type debugAIWatchMount = clidebug.DebugAIWatchMount
type controlEventSummary = clidebug.ControlEventSummary

func newDebugCmd() *cobra.Command {
	return clidebug.NewCommand(cliRuntime{})
}

func collectDebugAIReport(ctx context.Context, command, path, destinationPath string, eventLimit int, mountNames []string, allMounts bool) debugAIReport {
	return clidebug.CollectDebugAIReport(ctx, command, path, destinationPath, eventLimit, mountNames, allMounts)
}

func debugBundleFiles(path, destinationPath string, includeGoroutines, includeWatch bool) []string {
	return clidebug.DebugBundleFiles(path, destinationPath, includeGoroutines, includeWatch)
}

func addCollectDiagnostics(out *[]debugAIDiagnostic, report debugAIReport) {
	clidebug.AddCollectDiagnostics(out, report)
}

func watchDebugAI(ctx context.Context, path string, duration, interval time.Duration, eventLimit int, mountNames []string, allMounts bool, onSample func(debugAIWatchSample)) debugAIWatchReport {
	return clidebug.WatchDebugAI(ctx, path, duration, interval, eventLimit, mountNames, allMounts, onSample)
}

func validateDriverTestRequest(req contracttest.DriverTestRequest) error {
	return clidebug.ValidateDriverTestRequest(req)
}

func validateDriverBenchRequest(req contracttest.DriverTestRequest) error {
	return clidebug.ValidateDriverBenchRequest(req)
}
