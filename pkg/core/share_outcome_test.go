package core

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// TestShareOutcomeResult pins the provider-neutral fold: a synchronous batch, an
// asynchronous task handle, and a monitor-style handle all produce the same task
// shape, and partial success is never reported as a whole-batch failure.
func TestShareOutcomeResult(t *testing.T) {
	saved := func(id, name string, size int64) *drive.Entry {
		return &drive.Entry{ID: id, Name: name, Size: size}
	}
	failedItem := func(id string, code drive.ShareErrorCode) drive.ShareSaveItem {
		return drive.ShareSaveItem{
			SourceID:   id,
			SourceName: id + ".bin",
			SourceSize: 42,
			Error:      drive.NewShareError(code, "provider", "rejected"),
		}
	}

	cases := []struct {
		name       string
		outcome    drive.ShareSaveOutcome
		wantState  task.State
		wantItems  int
		wantFailed int
	}{
		{
			name: "synchronous batch all saved",
			outcome: drive.ShareSaveOutcome{State: drive.SaveStateSucceeded, Items: []drive.ShareSaveItem{
				{SourceID: "f1", Saved: saved("n1", "a.bin", 10)},
				{SourceID: "f2", Saved: saved("n2", "b.bin", 20)},
			}},
			wantState: task.StateSucceeded, wantItems: 2,
		},
		{
			name: "synchronous batch partially saved",
			outcome: drive.ShareSaveOutcome{State: drive.SaveStatePartial, Items: []drive.ShareSaveItem{
				{SourceID: "f1", Saved: saved("n1", "a.bin", 10)},
				failedItem("f2", drive.ShareErrQuota),
			}},
			wantState: task.StatePartialFailed, wantItems: 2, wantFailed: 1,
		},
		{
			name: "synchronous batch nothing saved",
			outcome: drive.ShareSaveOutcome{State: drive.SaveStateFailed, Items: []drive.ShareSaveItem{
				failedItem("f1", drive.ShareErrTicketInvalid),
				failedItem("f2", drive.ShareErrQuota),
			}},
			wantState: task.StateFailed, wantItems: 2, wantFailed: 2,
		},
		{
			name: "asynchronous task still running",
			outcome: drive.ShareSaveOutcome{State: drive.SaveStateRunning, Handle: "task-1", Items: []drive.ShareSaveItem{
				{SourceID: "f1", Saved: saved("n1", "a.bin", 10)},
			}},
			wantState: task.StateRunning, wantItems: 1,
		},
		{
			name: "monitor handle finished",
			outcome: drive.ShareSaveOutcome{State: drive.SaveStateSucceeded, Handle: "https://monitor/op", Items: []drive.ShareSaveItem{
				{SourceID: "f1", Saved: saved("n1", "a.bin", 10)},
			}},
			wantState: task.StateSucceeded, wantItems: 1,
		},
		{
			name:      "terminal outcome without items",
			outcome:   drive.ShareSaveOutcome{State: drive.SaveStateSucceeded},
			wantState: task.StateFailed, wantItems: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, tracking := shareOutcomeResult(tc.outcome)
			if state != tc.wantState {
				t.Fatalf("state = %s, want %s", state, tc.wantState)
			}
			if len(tracking.Items) != tc.wantItems {
				t.Fatalf("items = %d, want %d", len(tracking.Items), tc.wantItems)
			}
			failed := 0
			for _, item := range tracking.Items {
				if item.State == task.ItemStateFailed {
					failed++
					if item.Error == nil {
						t.Fatalf("failed item %s has no task error", item.ItemID)
					}
				}
				if item.ItemID == "" {
					t.Fatal("item lost its source id")
				}
			}
			if failed != tc.wantFailed {
				t.Fatalf("failed items = %d, want %d", failed, tc.wantFailed)
			}
		})
	}
}

// TestShareOutcomeResultKeepsEntryDetail verifies the item projection carries the
// saved entry's identity and the neutral error code.
func TestShareOutcomeResultKeepsEntryDetail(t *testing.T) {
	outcome := drive.ShareSaveOutcome{
		State: drive.SaveStatePartial,
		Items: []drive.ShareSaveItem{
			{SourceID: "f1", SourceName: "a.bin", SourceSize: 10, Saved: &drive.Entry{ID: "n1", Name: "a.bin", Size: 10}},
			{SourceID: "f2", SourceName: "b.bin", SourceSize: 20, Error: drive.NewShareError(drive.ShareErrVisitLimit, "45058", "limit")},
		},
	}
	_, tracking := shareOutcomeResult(outcome)
	first, second := tracking.Items[0], tracking.Items[1]
	if first.RemoteID != "n1" || first.Path != "a.bin" || first.SourceBytesTotal != 10 {
		t.Fatalf("saved item = %+v", first)
	}
	// visit-limit is deterministic: the task error must keep the neutral code
	// and must not be marked retryable.
	if second.Path != "b.bin" || second.Error == nil || second.Error.Code != "share_visit_limit" || second.Error.Retryable {
		t.Fatalf("failed item = %+v", second)
	}
}
