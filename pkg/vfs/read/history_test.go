package read

import (
	"fmt"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// The read history keeps summaries and details in separate rings so that a
// single read's detail burst cannot evict the summaries of earlier reads.

func TestHistoryKeepsSummariesWhenDetailsOverflow(t *testing.T) {
	var h historyState

	// A fixed floor rather than SummaryHistoryLimit, so the test fails if the
	// retention bound is lowered back to something that cannot hold multiple
	// complete reads.
	const retainedSummaries = 64
	for i := 0; i < retainedSummaries; i++ {
		h.AppendSummary(drive.MetricEvent{OpID: fmt.Sprintf("summary-%d", i), Phase: "read"})
	}
	// One read can produce an unbounded number of detail events (a whole-file
	// read walks every chunk), so overflow the detail ring several times over.
	for i := 0; i < DetailHistoryLimit*3+7; i++ {
		h.AppendDetail(drive.MetricEvent{ParentOpID: "summary-0", OpID: fmt.Sprintf("detail-%d", i), Phase: "cache_miss_load"})
	}

	snapshot := h.Snapshot()
	var summaries []drive.MetricEvent
	for _, event := range snapshot {
		if event.ParentOpID == "" {
			summaries = append(summaries, event)
		}
	}
	if len(summaries) != retainedSummaries {
		t.Fatalf("retained summaries = %d, want %d", len(summaries), retainedSummaries)
	}
	for i, event := range summaries {
		if want := fmt.Sprintf("summary-%d", i); event.OpID != want {
			t.Fatalf("summary[%d] = %q, want %q", i, event.OpID, want)
		}
	}

	if got := h.details.count; got != DetailHistoryLimit {
		t.Fatalf("retained details = %d, want %d", got, DetailHistoryLimit)
	}
}

func TestHistoryBoundsBothRingsIndependently(t *testing.T) {
	var h historyState

	for i := 0; i < DetailHistoryLimit+SummaryHistoryLimit+11; i++ {
		h.AppendSummary(drive.MetricEvent{OpID: fmt.Sprintf("summary-%d", i)})
		h.AppendDetail(drive.MetricEvent{ParentOpID: "p", OpID: fmt.Sprintf("detail-%d", i)})
	}

	snapshot := h.Snapshot()
	if len(snapshot) != SummaryHistoryLimit+DetailHistoryLimit {
		t.Fatalf("history length = %d, want %d", len(snapshot), SummaryHistoryLimit+DetailHistoryLimit)
	}
	if got := h.summaries.count; got != SummaryHistoryLimit {
		t.Fatalf("summary ring length = %d, want %d", got, SummaryHistoryLimit)
	}
	if got := h.details.count; got != DetailHistoryLimit {
		t.Fatalf("detail ring length = %d, want %d", got, DetailHistoryLimit)
	}
}

func TestHistorySnapshotReturnsAppendOrder(t *testing.T) {
	var h historyState

	// Interleave the two kinds so the merge order is observable: two details
	// recorded before a summary must appear before it.
	h.AppendDetail(drive.MetricEvent{OpID: "d0", ParentOpID: "p"})
	h.AppendDetail(drive.MetricEvent{OpID: "d1", ParentOpID: "p"})
	h.AppendSummary(drive.MetricEvent{OpID: "s0"})
	h.AppendDetail(drive.MetricEvent{OpID: "d2", ParentOpID: "p"})

	var got []string
	for _, event := range h.Snapshot() {
		got = append(got, event.OpID)
	}
	want := []string{"d0", "d1", "s0", "d2"}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("snapshot = %v, want %v", got, want)
		}
	}

	h.reset()
	if got := h.Snapshot(); len(got) != 0 {
		t.Fatalf("snapshot after reset = %v, want empty", got)
	}
}
