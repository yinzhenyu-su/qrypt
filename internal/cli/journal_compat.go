package cli

import (
	"io"

	"github.com/spf13/cobra"
	clijournal "github.com/yinzhenyu/qrypt/internal/cli/journal"
	"github.com/yinzhenyu/qrypt/pkg/config"
)

type debugCacheTarget = clijournal.DebugCacheTarget
type journalDebugReport = clijournal.DebugReport
type journalInvalidEntry = clijournal.InvalidEntry
type journalMaintenanceResult = clijournal.MaintenanceResult
type journalPendingDebug = clijournal.PendingDebug

func newJournalCmdWithUse(use string) *cobra.Command {
	return clijournal.NewCommand(cliRuntime{}, use)
}

func journalTargetsFromCmd(cmd *cobra.Command) ([]debugCacheTarget, error) {
	return clijournal.TargetsFromCmd(cliRuntime{}, cmd)
}

func debugCacheTargets(cacheDir string, cfg *config.Config, mountName string) ([]debugCacheTarget, error) {
	return clijournal.DebugCacheTargets(cliRuntime{}, cacheDir, cfg, mountName)
}

func inspectJournalCache(target debugCacheTarget) journalDebugReport {
	return clijournal.InspectCache(target)
}

func printJournalReport(w io.Writer, report journalDebugReport) {
	clijournal.PrintReport(w, report)
}
