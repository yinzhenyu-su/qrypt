package task

import "testing"

// TestShareDetailJoinsDurableKey pins the journaling contract: a share phase or
// link change must change the durable key, while transfer progress (which lives
// outside Detail) and unrelated detail keys must not.
func TestShareDetailJoinsDurableKey(t *testing.T) {
	base := Task{
		ID:   "share-1",
		Type: Type("share_save"),
		Detail: map[string]any{
			ShareDetailKey: map[string]any{"link_id": "abc", "phase": "save"},
		},
	}

	progressed := base
	progressed.Progress = Progress{TransferBytesDone: 1024, TransferBytesTotal: 8192, ItemsDone: 1}
	if taskDurableKey(base) != taskDurableKey(progressed) {
		t.Fatal("progress-only change altered the durable key")
	}

	moved := base
	moved.Detail = map[string]any{
		ShareDetailKey: map[string]any{"link_id": "abc", "phase": "download"},
	}
	if taskDurableKey(base) == taskDurableKey(moved) {
		t.Fatal("share phase change did not alter the durable key")
	}

	noisy := base
	noisy.Detail = map[string]any{
		ShareDetailKey:  base.Detail[ShareDetailKey],
		"progress_hint": "tick",
	}
	if taskDurableKey(base) != taskDurableKey(noisy) {
		t.Fatal("an unrelated detail key altered the durable key")
	}
}

// TestOfflineDetailStillJoinsDurableKey guards the pre-existing offline
// behaviour after the digest was generalized.
func TestOfflineDetailStillJoinsDurableKey(t *testing.T) {
	base := Task{
		ID:     "offline-1",
		Type:   TypeOfflineDownload,
		Detail: map[string]any{OfflineDetailKey: map[string]any{"provider_task_id": "p1"}},
	}
	moved := base
	moved.Detail = map[string]any{OfflineDetailKey: map[string]any{"provider_task_id": "p2"}}
	if taskDurableKey(base) == taskDurableKey(moved) {
		t.Fatal("offline provider id change did not alter the durable key")
	}
}
