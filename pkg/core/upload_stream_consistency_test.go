package core

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/task"
)

// A terminal item is authoritative: a remote observation may refresh the cloud
// diagnostics, but it must not resurrect a canceled item into running (nor let
// a later remote success turn it into succeeded).
func TestApplyRemoteUploadStateDoesNotRewriteTerminalItem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote task.State
	}{
		{"remote still running", task.StateRunning},
		{"remote still queued", task.StateQueued},
		{"remote succeeded", task.StateSucceeded},
		{"remote canceled", task.StateCanceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &uploadStreamItem{
				ID:       "item",
				DestPath: "/staged.txt",
				State:    task.ItemStateCanceled,
				Error:    &task.Error{Code: "canceled", Message: "task item canceled"},
			}
			remote := task.Task{
				ID:    "remote-1",
				State: tt.remote,
				Progress: task.Progress{
					Phase:           "upload",
					CloudBytesDone:  3,
					CloudBytesTotal: 7,
				},
			}

			if dismiss := applyRemoteUploadState(item, remote); dismiss {
				t.Fatal("a terminal item should not request remote dismissal")
			}
			if item.State != task.ItemStateCanceled {
				t.Fatalf("item state = %q, want canceled", item.State)
			}
			if item.Error == nil || item.Error.Code != "canceled" {
				t.Fatalf("item error = %+v, want the cancel error preserved", item.Error)
			}
			// Diagnostics still follow the remote task.
			if item.CloudTaskID != "remote-1" || item.CloudState != tt.remote {
				t.Fatalf("cloud observation = %q/%q, want remote-1/%q", item.CloudTaskID, item.CloudState, tt.remote)
			}
			if item.CloudWritten != 3 || item.CloudTotal != 7 || item.CloudPhase != "upload" {
				t.Fatalf("cloud progress = %d/%d %q, want 3/7 upload", item.CloudWritten, item.CloudTotal, item.CloudPhase)
			}
		})
	}
}

// Non-terminal items still follow the remote task, so the guard does not
// freeze the normal progress path.
func TestApplyRemoteUploadStateStillTracksNonTerminalItem(t *testing.T) {
	t.Parallel()
	item := &uploadStreamItem{ID: "item", DestPath: "/staged.txt", State: task.ItemStateRunning}
	remote := task.Task{ID: "remote-1", State: task.StateSucceeded, Progress: task.Progress{CloudBytesDone: 7, CloudBytesTotal: 7}}

	if dismiss := applyRemoteUploadState(item, remote); !dismiss {
		t.Fatal("remote success should request dismissal")
	}
	if item.State != task.ItemStateSucceeded {
		t.Fatalf("item state = %q, want succeeded", item.State)
	}
}

// newTestBatch builds a minimal upload stream batch for finishTask tests.
func newTestBatch(items ...*uploadStreamItem) *uploadStreamBatch {
	batch := &uploadStreamBatch{
		items:   items,
		channel: "staging",
	}
	for _, item := range items {
		batch.bytesTotal += item.Size
		if batch.byID == nil {
			batch.byID = map[string]*uploadStreamItem{}
		}
		batch.byID[item.ID] = item
	}
	return batch
}

// finishTask must never publish a terminal task state alongside a live item:
// with the runner gone, an in-flight item cannot progress, so it is converged
// to failed and the task's terminal state is decided from real outcomes.
func TestUploadStreamFinishConvergesInFlightItems(t *testing.T) {
	t.Parallel()
	item := &uploadStreamItem{ID: "item", DestPath: "/staged.txt", State: task.ItemStateRunning, Size: 7, Written: 7}
	batch := newTestBatch(item)

	var published *task.Task
	update := func(apply func(*task.Task)) {
		published = &task.Task{Detail: map[string]any{}}
		apply(published)
	}

	err := batch.finishTask(update)
	if published == nil {
		t.Fatal("finishTask published no snapshot")
	}
	if err == nil {
		t.Fatal("a task whose only item was abandoned should report the failure to its runner")
	}
	if published.Progress.ItemsDone != 1 || published.Progress.ItemsFailed != 1 {
		t.Fatalf("progress = %d done / %d failed, want 1/1", published.Progress.ItemsDone, published.Progress.ItemsFailed)
	}
	if len(published.Tracking.Items) != 1 {
		t.Fatalf("published items = %+v, want one", published.Tracking.Items)
	}
	publishedItem := published.Tracking.Items[0]
	if !isTerminalStreamItem(publishedItem.State) {
		t.Fatalf("published item state = %q, want terminal", publishedItem.State)
	}
	if publishedItem.State != task.ItemStateFailed || publishedItem.Error == nil || publishedItem.Error.Code != "abandoned" {
		t.Fatalf("published item = %+v, want failed with an abandoned reason", publishedItem)
	}
	if publishedItem.Capabilities.Cancelable {
		t.Fatalf("abandoned item capabilities = %+v, want not cancelable", publishedItem.Capabilities)
	}
	if item.State != task.ItemStateFailed {
		t.Fatalf("in-flight item left at %q, want failed", item.State)
	}
}

// A finished-but-partial batch still reports partial_failed, now with counters
// that agree with the item states.
func TestUploadStreamFinishReportsPartialFailureWithConsistentCounters(t *testing.T) {
	t.Parallel()
	done := &uploadStreamItem{ID: "done", DestPath: "/done.txt", State: task.ItemStateSucceeded, Size: 4, Written: 4}
	live := &uploadStreamItem{ID: "live", DestPath: "/live.txt", State: task.ItemStateRunning, Size: 7, Written: 7}
	batch := newTestBatch(done, live)

	var published *task.Task
	update := func(apply func(*task.Task)) {
		published = &task.Task{Detail: map[string]any{}}
		apply(published)
	}

	if err := batch.finishTask(update); err != nil {
		t.Fatalf("partial failure should not fail the runner: %v", err)
	}
	if published.State != task.StatePartialFailed {
		t.Fatalf("task state = %q, want partial_failed", published.State)
	}
	if published.Progress.ItemsDone != 2 || published.Progress.ItemsFailed != 1 {
		t.Fatalf("progress = %d done / %d failed, want 2/1", published.Progress.ItemsDone, published.Progress.ItemsFailed)
	}
	for _, publishedItem := range published.Tracking.Items {
		if !isTerminalStreamItem(publishedItem.State) {
			t.Fatalf("published item = %+v, want every item terminal", publishedItem)
		}
	}
}
