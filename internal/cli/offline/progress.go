package offline

import (
	"fmt"
	"io"
	"strings"

	"github.com/yinzhenyu/qrypt/internal/cli/i18n"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// offlineProgress renders an offline task's phase and byte counters as a single
// updating line. It is shared by `fetch --wait` and `offline wait` so a blocking
// command never looks hung. It writes to stderr only, so `--json` output stays a
// single document on stdout.
type offlineProgress struct {
	out      io.Writer
	loc      *i18n.Localizer
	last     string
	rendered bool
}

func newOfflineProgress(out io.Writer, loc *i18n.Localizer) *offlineProgress {
	return &offlineProgress{out: out, loc: loc}
}

// render draws the current progress, skipping a line identical to the previous
// one so a slow task does not spam the terminal.
func (p *offlineProgress) render(item task.Task) {
	if p == nil {
		return
	}
	line := offlineProgressLine(item, p.loc)
	if line == "" || line == p.last {
		return
	}
	p.last = line
	p.rendered = true
	fmt.Fprintf(p.out, "\r%s", line)
}

// finish terminates the updating line so later output starts cleanly.
func (p *offlineProgress) finish() {
	if p == nil || !p.rendered {
		return
	}
	fmt.Fprintln(p.out)
	p.rendered = false
}

// offlineProgressLine formats one progress line for a task.
func offlineProgressLine(item task.Task, loc *i18n.Localizer) string {
	phase := strings.TrimSpace(string(item.Progress.Phase))
	if phase == "" {
		phase = string(item.State)
	}
	parts := []string{phase}
	switch {
	case item.Progress.TransferBytesTotal > 0:
		parts = append(parts, fmt.Sprintf("%s/%s (%d%%)",
			cliruntime.FormatBytes(item.Progress.TransferBytesDone),
			cliruntime.FormatBytes(item.Progress.TransferBytesTotal),
			offlinePercent(item.Progress.TransferBytesDone, item.Progress.TransferBytesTotal)))
	case item.Progress.CloudBytesTotal > 0:
		parts = append(parts, fmt.Sprintf("%s/%s (%d%%)",
			cliruntime.FormatBytes(item.Progress.CloudBytesDone),
			cliruntime.FormatBytes(item.Progress.CloudBytesTotal),
			offlinePercent(item.Progress.CloudBytesDone, item.Progress.CloudBytesTotal)))
	}
	if item.Progress.ItemsTotal > 0 {
		parts = append(parts, loc.T("out.offline.progress_files", item.Progress.ItemsDone, item.Progress.ItemsTotal))
	}
	return "  " + strings.Join(parts, "  ")
}

func offlinePercent(done, total int64) int64 {
	if total <= 0 {
		return 0
	}
	percent := done * 100 / total
	switch {
	case percent < 0:
		return 0
	case percent > 100:
		return 100
	default:
		return percent
	}
}

// offlineTaskDone reports whether a task state is terminal.
func offlineTaskDone(state task.State) bool {
	switch state {
	case task.StateSucceeded, task.StatePartialFailed, task.StateFailed, task.StateCanceled:
		return true
	default:
		return false
	}
}
