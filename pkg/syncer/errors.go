package sync

import (
	"fmt"
	"os"
)

// ReportFailure prints one failed sync op to stderr. Kept in the domain
// package so executor and orchestration share the same error format.
func ReportFailure(op, path string, err error) {
	fmt.Fprintf(os.Stderr, "sync %s %s: %v\n", op, path, err)
}

// PrintSummary writes the human-readable sync summary to w.
func PrintSummary(w interface {
	Write([]byte) (int, error)
}, result Result) {
	verb := "would sync"
	if !result.DryRun {
		verb = "synced"
	}
	fmt.Fprintf(w, "%s %s -> %s\n", verb, result.Source, result.Destination)
	fmt.Fprintf(w, "  add: %d, update: %d, delete: %d, skip: %d, conflict: %d, failed: %d\n",
		result.Summary.Adds, result.Summary.Update, result.Summary.Delete,
		result.Summary.Skip, result.Summary.Conflict, result.Summary.Failed)
	if !result.DryRun || result.Summary.Bytes > 0 {
		fmt.Fprintf(w, "  bytes: %d\n", result.Summary.Bytes)
	}
	for _, entry := range result.Entries {
		if entry.Action == ActionSkip && result.DryRun {
			continue
		}
		detail := entry.Reason
		if entry.Bytes > 0 {
			detail = fmt.Sprintf("%s (%d bytes)", detail, entry.Bytes)
		}
		fmt.Fprintf(w, "  [%s] %s %s\n", entry.Action, entry.Path, detail)
	}
}
