package fs

import (
	"fmt"
	"io"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	syncer "github.com/yinzhenyu/qrypt/pkg/syncer"
)

// PrintSyncSummary writes the human-readable sync summary. It lives in the CLI
// layer so the domain package stays free of internal localization imports.
func PrintSyncSummary(w io.Writer, result syncer.Result, loc *i18n.Localizer) {
	verbKey := "out.sync.synced"
	if result.DryRun {
		verbKey = "out.sync.would"
	}
	fmt.Fprintf(w, "%s\n", loc.T(verbKey, result.Source, result.Destination))
	fmt.Fprintf(w, "  %s\n", loc.T("out.sync.summary",
		result.Summary.Adds, result.Summary.Update, result.Summary.Delete,
		result.Summary.Skip, result.Summary.Conflict, result.Summary.Failed))
	if !result.DryRun || result.Summary.Bytes > 0 {
		fmt.Fprintf(w, "  %s\n", loc.T("out.sync.bytes", result.Summary.Bytes))
	}
	for _, entry := range result.Entries {
		if entry.Action == syncer.ActionSkip && result.DryRun {
			continue
		}
		detail := entry.Reason
		if entry.Bytes > 0 {
			detail = loc.T("out.sync.entry_bytes", detail, entry.Bytes)
		}
		fmt.Fprintf(w, "  [%s] %s %s\n", entry.Action, entry.Path, detail)
	}
}
