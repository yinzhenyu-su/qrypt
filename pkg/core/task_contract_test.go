package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	"github.com/yinzhenyu/qrypt/pkg/task"
	"github.com/yinzhenyu/qrypt/pkg/vfs"
)

// The task slice of the mobile boundary is owned as aliases: core re-exports
// the pkg/task DTOs (Task, TaskFilter, TaskRequest, TaskEvent, ...) so the
// mobile wire format is identical to pkg/task by construction, and mobile
// consumes subscriptions through the core TaskSubscription interface instead
// of the concrete *task.Subscription. These tests pin that design:
//
//   - the aliases are true aliases (identity, not duplicated structs);
//   - the legacy Core.OpenTaskEvents/OpenTaskEventsFrom signatures stay
//     concrete (the pkg/control debug server contract depends on them);
//   - the mobile-facing JSON wire shape is exactly the pinned schema;
//   - event replay/sequence/close behavior survives at the boundary.

// TestCoreTaskContractsAliasTaskTypes pins that the core task contracts are
// aliases of the pkg/task types. If a future change replaces an alias with a
// duplicated struct (the media DTO pattern), this fails loudly, forcing that
// change to be reviewed for wire-format and conversion cost.
func TestCoreTaskContractsAliasTaskTypes(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		core, task reflect.Type
	}{
		{reflect.TypeOf(Task{}), reflect.TypeOf(task.Task{})},
		{reflect.TypeOf(TaskFilter{}), reflect.TypeOf(task.Filter{})},
		{reflect.TypeOf(TaskItem{}), reflect.TypeOf(task.Item{})},
		{reflect.TypeOf(TaskItemTracking{}), reflect.TypeOf(task.ItemTracking{})},
		{reflect.TypeOf(TaskItemFilter{}), reflect.TypeOf(task.ItemFilter{})},
		{reflect.TypeOf(TaskOptions{}), reflect.TypeOf(task.Options{})},
		{reflect.TypeOf(TaskOperationRequest{}), reflect.TypeOf(task.OperationRequest{})},
		{reflect.TypeOf(TaskRequest{}), reflect.TypeOf(task.Request{})},
		{reflect.TypeOf(TaskState("")), reflect.TypeOf(task.State(""))},
		{reflect.TypeOf(TaskType("")), reflect.TypeOf(task.Type(""))},
		{reflect.TypeOf(TaskEvent{}), reflect.TypeOf(task.Event{})},
	}
	for _, tc := range pairs {
		if tc.core != tc.task {
			t.Fatalf("core contract %v must alias task type %v (alias boundary, not a duplicated DTO)", tc.core, tc.task)
		}
	}
	// The concrete subscription returned by the legacy core methods satisfies
	// the client interface mobile consumes.
	var _ TaskSubscription = (*task.Subscription)(nil)
	if got := reflect.TypeOf(Task{}).PkgPath(); got != "github.com/yinzhenyu/qrypt/pkg/task" {
		t.Fatalf("Task definition package = %q, want pkg/task (the task model owns the schema)", got)
	}
	if TaskScopeUser != task.ScopeUser {
		t.Fatalf("TaskScopeUser = %q, want %q", TaskScopeUser, task.ScopeUser)
	}
	if TaskScopeInternal != task.ScopeInternal {
		t.Fatalf("TaskScopeInternal = %q, want %q", TaskScopeInternal, task.ScopeInternal)
	}
	if TaskTypeUploadStreamBatch != task.TypeUploadStreamBatch {
		t.Fatalf("TaskTypeUploadStreamBatch = %q, want %q", TaskTypeUploadStreamBatch, task.TypeUploadStreamBatch)
	}
}

// TestTaskScopeWireValuesPinned pins the scope wire values: "sync" stays a
// compatibility contract even though only the syncer produces it, and
// "internal" is the mount bookkeeping origin carried on the wire. Both
// serialization directions are pinned so old consumers keep reading the
// values they know.
func TestTaskScopeWireValuesPinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scope task.Scope
		want  string
	}{
		{task.ScopeUser, "user"},
		{task.ScopeSync, "sync"},
		{task.ScopeInternal, "internal"},
	} {
		if string(tc.scope) != tc.want {
			t.Fatalf("scope wire value = %q, want %q", tc.scope, tc.want)
		}
		data, err := json.Marshal(task.Task{ID: "t", Type: task.TypeUploadRemote, State: task.StateQueued, Scope: tc.scope})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"scope":"`+tc.want+`"`) {
			t.Fatalf("marshaled task %s, want %q scope", data, tc.want)
		}
		var got task.Task
		if err := json.Unmarshal([]byte(`{"id":"t","type":"upload_remote","state":"queued","scope":"`+tc.want+`"}`), &got); err != nil {
			t.Fatal(err)
		}
		if got.Scope != tc.scope {
			t.Fatalf("unmarshaled scope = %q, want %q", got.Scope, tc.scope)
		}
	}
}

// TestTaskVisibilityWireValuesPinned pins the visibility wire values and
// their JSON form (additive contract key: "visibility").
func TestTaskVisibilityWireValuesPinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		visibility task.Visibility
		want       string
	}{
		{task.VisibilityVisible, "visible"},
		{task.VisibilityHidden, "hidden"},
	} {
		if string(tc.visibility) != tc.want {
			t.Fatalf("visibility wire value = %q, want %q", tc.visibility, tc.want)
		}
		data, err := json.Marshal(task.Task{ID: "t", Type: task.TypeUploadRemote, State: task.StateQueued, Visibility: tc.visibility})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"visibility":"`+tc.want+`"`) {
			t.Fatalf("marshaled task %s, want %q visibility", data, tc.want)
		}
	}
}

// TestTaskPhaseWireValuesPinned pins the display vocabulary of the phase
// labels (glossary: 阶段标签). The vocabulary is declared independently of
// the State enums — no phase value is written from a State constant — and
// these wire strings are what mobile/CLI consumers display.
func TestTaskPhaseWireValuesPinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase task.Phase
		want  string
	}{
		{task.PhasePending, "pending"},
		{task.PhaseScheduled, "scheduled"},
		{task.PhaseRetrying, "retrying"},
		{task.PhaseReady, "ready"},
		{task.PhaseAppStaging, "staging"},
		{task.PhaseServerStage, "stage"},
		{task.PhaseUpload, "upload"},
		{task.PhaseDownload, "download"},
		{task.PhaseMkdir, "mkdir"},
		{task.PhaseCopy, "copy"},
		{task.PhaseMove, "move"},
		{task.PhaseDelete, "delete"},
		{task.PhaseDeleteSource, "delete_source"},
		{task.PhaseSkipped, "skipped"},
		{task.PhaseInstant, "instant"},
		{task.PhaseDirect, "direct"},
		{task.PhaseQueuedUpload, "queued_upload"},
		{task.PhaseHashing, "hashing"},
		{task.PhaseComplete, "complete"},
		{task.PhaseCloudUploading, "uploading"},
		{task.PhaseCloudCompleted, "completed"},
		{task.PhaseRecordStarting, "starting"},
		{task.PhaseRecordSuperseded, "superseded"},
		{task.PhasePartialFailed, "partial_failed"},
		{task.PhaseFailed, "failed"},
		{task.PhaseCanceled, "canceled"},
	} {
		if string(tc.phase) != tc.want {
			t.Fatalf("phase wire value = %q, want %q", tc.phase, tc.want)
		}
	}
}

// TestTaskStateWireValuesPinned pins the task-level state wire values. The
// handshake strings ("waiting_input"/"waiting_output") are intentionally not
// among them: they are item-level only (glossary: 等待供数 / 等待取数).
func TestTaskStateWireValuesPinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state task.State
		want  string
	}{
		{task.StateQueued, "queued"},
		{task.StateScheduled, "scheduled"},
		{task.StateRunning, "running"},
		{task.StateRetryWait, "retry_wait"},
		{task.StateCanceling, "canceling"},
		{task.StateSucceeded, "succeeded"},
		{task.StatePartialFailed, "partial_failed"},
		{task.StateFailed, "failed"},
		{task.StateCanceled, "canceled"},
	} {
		if string(tc.state) != tc.want {
			t.Fatalf("task state wire value = %q, want %q", tc.state, tc.want)
		}
	}
}

// TestItemStateWireValuesPinned pins the item-level state wire values,
// including the stream handshake states (glossary: 等待供数 / 等待取数), both
// as Go values and as the JSON strings consumers read.
func TestItemStateWireValuesPinned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state task.ItemState
		want  string
	}{
		{task.ItemStateQueued, "queued"},
		{task.ItemStateRunning, "running"},
		{task.ItemStateRetryWait, "retry_wait"},
		{task.ItemStateWaitingInput, "waiting_input"},
		{task.ItemStateWaitingOutput, "waiting_output"},
		{task.ItemStateSucceeded, "succeeded"},
		{task.ItemStateFailed, "failed"},
		{task.ItemStateCanceled, "canceled"},
	} {
		if string(tc.state) != tc.want {
			t.Fatalf("item state wire value = %q, want %q", tc.state, tc.want)
		}
		data, err := json.Marshal(task.ItemTracking{ItemID: "i", State: tc.state})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"state":"`+tc.want+`"`) {
			t.Fatalf("marshaled item %s, want %q state", data, tc.want)
		}
	}
}

// TestCoreTaskEventMethodsKeepLegacySignatures pins the exact exported
// signatures of the legacy task-event entry points. The pkg/control debug
// server contract requires OpenTaskEventsFrom to return the concrete
// *task.Subscription; mobile consumes the same calls through the
// TaskSubscription interface instead (see TestCoreTaskContractsAliasTaskTypes).
// The method values are compared against explicitly typed function values so
// the pin is exact and compile-time deterministic.
func TestCoreTaskEventMethodsKeepLegacySignatures(t *testing.T) {
	t.Parallel()
	wantOpen := reflect.TypeOf(func(*Core, context.Context, task.Filter) (*task.Subscription, error) { panic("pinned type") })
	wantFrom := reflect.TypeOf(func(*Core, context.Context, task.Filter, uint64) (*task.Subscription, error) { panic("pinned type") })
	if got := reflect.TypeOf((*Core).OpenTaskEvents); got != wantOpen {
		t.Fatalf("OpenTaskEvents type = %v, want func(*Core, context.Context, task.Filter) (*task.Subscription, error)", got)
	}
	if got := reflect.TypeOf((*Core).OpenTaskEventsFrom); got != wantFrom {
		t.Fatalf("OpenTaskEventsFrom type = %v, want func(*Core, context.Context, task.Filter, uint64) (*task.Subscription, error)", got)
	}
}

// requireSameJSONTags pins that each core task alias marshals with exactly
// the pkg/task field names and json tags. For today's aliases this holds by
// construction; the check becomes meaningful if an alias is ever converted
// into an independent struct.
func requireSameJSONTags(t *testing.T, coreType, taskType reflect.Type) {
	t.Helper()
	if coreType.NumField() != taskType.NumField() {
		t.Fatalf("field count mismatch: %v has %d fields, %v has %d", coreType, coreType.NumField(), taskType, taskType.NumField())
	}
	for i := 0; i < coreType.NumField(); i++ {
		cf, tf := coreType.Field(i), taskType.Field(i)
		if cf.Name != tf.Name {
			t.Fatalf("field %d name mismatch: core %q vs task %q", i, cf.Name, tf.Name)
		}
		if cf.Tag.Get("json") != tf.Tag.Get("json") {
			t.Fatalf("json tag mismatch on %v.%s: %q vs task %q", coreType, cf.Name, cf.Tag.Get("json"), tf.Tag.Get("json"))
		}
	}
}

func TestCoreTaskContractJSONTagsMatchTask(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct {
		core, task reflect.Type
	}{
		{reflect.TypeOf(Task{}), reflect.TypeOf(task.Task{})},
		{reflect.TypeOf(TaskFilter{}), reflect.TypeOf(task.Filter{})},
		{reflect.TypeOf(TaskItem{}), reflect.TypeOf(task.Item{})},
		{reflect.TypeOf(TaskItemTracking{}), reflect.TypeOf(task.ItemTracking{})},
		{reflect.TypeOf(TaskItemFilter{}), reflect.TypeOf(task.ItemFilter{})},
		{reflect.TypeOf(TaskOptions{}), reflect.TypeOf(task.Options{})},
		{reflect.TypeOf(TaskOperationRequest{}), reflect.TypeOf(task.OperationRequest{})},
		{reflect.TypeOf(TaskRequest{}), reflect.TypeOf(task.Request{})},
		{reflect.TypeOf(TaskEvent{}), reflect.TypeOf(task.Event{})},
	} {
		requireSameJSONTags(t, pair.core, pair.task)
	}
}

// TestCoreTaskWireJSONPinned locks the mobile-facing JSON wire shape of the
// task DTOs. Regenerate the literals only when the schema itself changes
// deliberately (mobile is a compatibility surface).
func TestCoreTaskWireJSONPinned(t *testing.T) {
	t.Parallel()
	wantTask := `{"id":"t-1","operation":"upload","type":"upload_stream_batch","state":"running","scope":"user","mount":"quark","path":"/quark/a.bin","name":"a.bin","created_at":"2026-01-02T03:04:05Z","started_at":"2026-01-02T03:04:06Z","updated_at":"2026-01-02T03:04:07Z","completed_at":"2026-01-02T03:04:08Z","retry_count":3,"next_attempt_at":"2026-01-02T03:05:00Z","version":9,"schema_version":1,"execution_generation":2,"idempotency_key":"idem-1","operation_fingerprint":"fp-1","dismiss_requested":true,"progress":{"source_bytes_done":10,"source_bytes_total":20,"cloud_bytes_done":5,"cloud_bytes_total":20,"staging_bytes_done":12,"staging_bytes_total":20,"output_bytes_done":3,"output_bytes_total":20,"transfer_bytes_done":8,"transfer_bytes_total":20,"items_done":1,"items_total":2,"items_failed":1,"current_path":"/quark/a.bin","phase":"upload","speed_bps":1234,"eta_ms":5678},"capabilities":{"cancelable":true,"retryable":true,"dismissible":true,"persistent":true,"actions":["cancel","retry"]},"error":{"code":"local_io","message":"boom","retryable":true},"result":{"items":[{"path":"/quark/a.bin","item_id":"local-1","source_path":"src","dest_path":"/quark/a.bin","mount":"quark","state":"running","phase":"staging","error":{"code":"x","message":"y"},"remote_id":"rid","source_bytes_done":1,"source_bytes_total":2,"cloud_bytes_done":3,"cloud_bytes_total":4,"staging_bytes_done":5,"staging_bytes_total":6,"output_bytes_done":7,"output_bytes_total":8,"transfer_bytes_done":9,"transfer_bytes_total":10,"resume_offset":11,"capabilities":{"open_input":true,"commit_input":true,"open_output":true,"cancelable":true,"actions":["cancel"]}}]},"detail":{"auto":"x"}}`
	at := func(y int, m time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, m, d, h, mi, s, 0, time.UTC)
	}
	got, err := json.Marshal(Task{
		ID:                   "t-1",
		Operation:            task.OperationUpload,
		Type:                 TaskTypeUploadStreamBatch,
		State:                task.StateRunning,
		Scope:                TaskScopeUser,
		Mount:                "quark",
		Path:                 "/quark/a.bin",
		Name:                 "a.bin",
		CreatedAt:            at(2026, 1, 2, 3, 4, 5),
		StartedAt:            at(2026, 1, 2, 3, 4, 6),
		UpdatedAt:            at(2026, 1, 2, 3, 4, 7),
		CompletedAt:          at(2026, 1, 2, 3, 4, 8),
		RetryCount:           3,
		NextAttempt:          at(2026, 1, 2, 3, 5, 0),
		Version:              9,
		SchemaVersion:        1,
		ExecutionGeneration:  2,
		IdempotencyKey:       "idem-1",
		OperationFingerprint: "fp-1",
		DismissRequested:     true,
		Progress: task.Progress{
			SourceBytesDone: 10, SourceBytesTotal: 20,
			CloudBytesDone: 5, CloudBytesTotal: 20,
			StagingBytesDone: 12, StagingBytesTotal: 20,
			OutputBytesDone: 3, OutputBytesTotal: 20,
			TransferBytesDone: 8, TransferBytesTotal: 20,
			ItemsDone: 1, ItemsTotal: 2, ItemsFailed: 1,
			CurrentPath: "/quark/a.bin",
			Phase:       "upload",
			SpeedBPS:    1234,
			ETAMS:       5678,
		},
		Capabilities: task.Capabilities{
			Cancelable: true, Retryable: true, Dismissible: true, Persistent: true,
			Actions: []task.Action{task.ActionCancel, task.ActionRetry},
		},
		Error: &task.Error{Code: "local_io", Message: "boom", Retryable: true},
		Tracking: task.Tracking{Items: []task.ItemTracking{{
			Path: "/quark/a.bin", ItemID: "local-1", SourcePath: "src",
			DestPath: "/quark/a.bin", Mount: "quark", State: task.ItemStateRunning,
			Phase: task.PhaseAppStaging, Error: &task.Error{Code: "x", Message: "y"}, RemoteID: "rid",
			SourceBytesDone: 1, SourceBytesTotal: 2, CloudBytesDone: 3, CloudBytesTotal: 4,
			StagingBytesDone: 5, StagingBytesTotal: 6, OutputBytesDone: 7, OutputBytesTotal: 8,
			TransferBytesDone: 9, TransferBytesTotal: 10, ResumeOffset: 11,
			Capabilities: task.ItemCapabilities{
				OpenInput: true, CommitInput: true, OpenOutput: true, Cancelable: true,
				Actions: []task.Action{task.ActionCancel},
			},
		}}},
		Detail: map[string]any{"auto": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != wantTask {
		t.Fatalf("task wire JSON changed:\n got %s\nwant %s", got, wantTask)
	}

	wantEvent := `{"seq":42,"type":"task_updated","task_id":"t-1","snapshot_required":true,"from_seq":40,"to_seq":41}`
	gotEvent, err := json.Marshal(TaskEvent{
		Seq:              42,
		Type:             task.EventTaskUpdated,
		TaskID:           "t-1",
		SnapshotRequired: true,
		FromSeq:          40,
		ToSeq:            41,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(gotEvent) != wantEvent {
		t.Fatalf("task event wire JSON changed:\n got %s\nwant %s", gotEvent, wantEvent)
	}

	// A populated event payload must be byte-identical to the task package's
	// own encoding of the same value: the boundary cannot drift the payload.
	taskEvent, err := json.Marshal(task.Event{
		Seq:              42,
		Type:             task.EventTaskUpdated,
		TaskID:           "t-1",
		SnapshotRequired: true,
		FromSeq:          40,
		ToSeq:            41,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotEvent, taskEvent) {
		t.Fatalf("core event JSON %s != task event JSON %s", gotEvent, taskEvent)
	}
}

// newTaskBoundaryCore builds a Core over a localfs mount for task-boundary
// tests, mirrors the fixture in upload_stream_task_test.go.
func newTaskBoundaryCore(t *testing.T) *Core {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	remote := t.TempDir()
	fs, err := vfs.New(localfs.New(remote), vfs.Options{
		StorageDir:  filepath.Join(t.TempDir(), "cache"),
		UploadDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopTestVFS(t, fs) })
	fs.Start(ctx)
	return newTestCore(t, fs)
}

func createBoundaryUploadTask(t *testing.T, c *Core, itemID string) Task {
	t.Helper()
	created, err := c.CreateTask(context.Background(), TaskRequest{
		Type:  TaskTypeUploadStreamBatch,
		Items: []TaskItem{{ItemID: itemID, DestPath: "/" + itemID + ".txt", Size: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// readTaskEventsUntil drains a subscription until match reports true,
// failing if the deadline elapses first.
func readTaskEventsUntil(t *testing.T, sub TaskSubscription, match func(TaskEvent) bool) []TaskEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out []TaskEvent
	for {
		events, err := sub.Read(ctx)
		if err != nil {
			t.Fatalf("read task events: %v", err)
		}
		out = append(out, events...)
		for _, event := range events {
			if match(event) {
				return out
			}
		}
	}
}

// TestCoreTaskEventReplaySequenceAfterClose pins the task-event lifecycle at
// the mobile boundary: subscriptions open from core, sequence numbers grow
// strictly, closing the subscription is idempotent, and reopening from the
// last seen sequence replays exactly the events that follow it.
func TestCoreTaskEventReplaySequenceAfterClose(t *testing.T) {
	t.Parallel()
	c := newTaskBoundaryCore(t)
	filter := TaskFilter{Types: []TaskType{TaskTypeUploadStreamBatch}}

	sub, err := c.OpenTaskEventsFrom(context.Background(), filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := createBoundaryUploadTask(t, c, "first")
	seen := readTaskEventsUntil(t, sub, func(event TaskEvent) bool {
		return event.Type == task.EventTaskUpdated && event.TaskID == first.ID
	})
	if len(seen) == 0 {
		t.Fatal("no events seen")
	}
	for i, event := range seen {
		if event.Type != task.EventTaskUpdated {
			t.Fatalf("event %d type = %s, want task_updated", i, event.Type)
		}
		if i > 0 && event.Seq <= seen[i-1].Seq {
			t.Fatalf("event %d seq %d not after seq %d", i, event.Seq, seen[i-1].Seq)
		}
		if event.SnapshotRequired || event.FromSeq != 0 || event.ToSeq != 0 {
			t.Fatalf("live subscription emitted a gap event: %+v", event)
		}
	}
	lastSeq := seen[len(seen)-1].Seq

	// Closing is idempotent; reads after close surface the closed channel.
	//
	// Drain rather than checking the first read: Subscription.Read delivers
	// whatever Close left buffered and reports context.Canceled only on the
	// read that finds the channel both closed and empty. Events the task
	// produced between the assertion above and the Close are exactly that
	// buffer, and whether any of them exist by now is a matter of scheduling.
	sub.Close()
	sub.Close()
	for drained := 0; ; drained++ {
		_, err := sub.Read(context.Background())
		if errors.Is(err, context.Canceled) {
			break
		}
		if err != nil {
			t.Fatalf("read after close err = %v, want context.Canceled", err)
		}
		if drained > 1000 {
			t.Fatal("read after close never surfaced the closed channel")
		}
	}

	// Reopen from the last seen sequence: only strictly newer events replay,
	// starting at the very next sequence.
	resumed, err := c.OpenTaskEventsFrom(context.Background(), filter, lastSeq)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	second := createBoundaryUploadTask(t, c, "second")
	replayed := readTaskEventsUntil(t, resumed, func(event TaskEvent) bool {
		return event.Type == task.EventTaskUpdated && event.TaskID == second.ID
	})
	if replayed[0].Seq != lastSeq+1 {
		t.Fatalf("first replayed seq = %d, want %d (contiguous replay from afterSeq)", replayed[0].Seq, lastSeq+1)
	}
	for i, event := range replayed {
		if event.Seq <= lastSeq {
			t.Fatalf("replayed event %d seq %d <= lastSeq %d", i, event.Seq, lastSeq)
		}
		if i > 0 && event.Seq <= replayed[i-1].Seq {
			t.Fatalf("replayed event %d seq %d not after seq %d", i, event.Seq, replayed[i-1].Seq)
		}
	}
}

// TestCoreTaskSubscriptionReadCloseRace exercises concurrent reads, manager
// broadcasts and Close on the subscription the boundary hands to mobile.
// Run under -race; the assertion after the group pins that a closed
// subscription reports context.Canceled rather than panicking.
func TestCoreTaskSubscriptionReadCloseRace(t *testing.T) {
	t.Parallel()
	c := newTaskBoundaryCore(t)
	sub, err := c.OpenTaskEventsFrom(context.Background(), TaskFilter{Types: []TaskType{TaskTypeUploadStreamBatch}}, 0)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				_, _ = sub.Read(ctx) // the race detector is the oracle
				cancel()
			}
		}()
	}
	for i := 0; i < 3; i++ {
		createBoundaryUploadTask(t, c, "race-"+string(rune('a'+i)))
	}
	sub.Close()
	sub.Close() // idempotent under concurrency
	wg.Wait()

	// Drain rather than checking the first read: the three tasks above queued
	// events that Close does not discard, and Subscription.Read only reports
	// context.Canceled on the read that finds the channel closed and empty.
	for drained := 0; ; drained++ {
		_, err := sub.Read(context.Background())
		if errors.Is(err, context.Canceled) {
			break
		}
		if err != nil {
			t.Fatalf("read after close err = %v, want context.Canceled", err)
		}
		if drained > 1000 {
			t.Fatal("read after close never surfaced the closed channel")
		}
	}
}
