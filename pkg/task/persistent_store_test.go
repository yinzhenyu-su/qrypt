package task

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentStoreRejectsFutureJournalVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	if err := os.WriteFile(path, []byte("{\"version\":2,\"op\":\"update\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentStore(path); err == nil {
		t.Fatal("NewPersistentStore() succeeded for a future journal version")
	}
}

func TestPersistentStorePersistsOnlyPersistentTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(ManagedTask{Task: Task{
		ID:    "persistent",
		Type:  TypeDeleteBatch,
		State: StatePartialFailed,
		Capabilities: Capabilities{
			Persistent: true,
		},
		Tracking: Tracking{Items: []ItemTracking{{
			Path:  "/missing.txt",
			State: ItemStateFailed,
			Error: &Error{Message: "missing"},
		}}},
	}})
	store.PutManaged(ManagedTask{Task: Task{
		ID:    "memory-only",
		Type:  TypeMoveRemote,
		State: StateSucceeded,
	}})

	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.GetManaged("memory-only"); ok {
		t.Fatal("memory-only task was persisted")
	}
	got, ok := reopened.GetManaged("persistent")
	if !ok {
		t.Fatal("persistent task missing after replay")
	}
	if got.Task.State != StatePartialFailed || len(got.Task.Tracking.Items) != 1 || got.Task.Tracking.Items[0].Error == nil {
		t.Fatalf("replayed task = %+v", got.Task)
	}
}

func TestPersistentStoreReplaysRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(ManagedTask{Task: Task{
		ID:    "task-1",
		Type:  TypeDeleteBatch,
		State: StateSucceeded,
		Capabilities: Capabilities{
			Persistent:  true,
			Dismissible: true,
		},
	}})
	if !store.DismissManaged("task-1") {
		t.Fatal("DismissManaged returned false")
	}
	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.GetManaged("task-1"); ok {
		t.Fatal("removed task replayed")
	}
}

func TestPersistentStoreMarksActiveTasksInterruptedOnReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(ManagedTask{Task: Task{
		ID:    "running",
		Type:  TypeDownload,
		State: StateRunning,
		Capabilities: Capabilities{
			Cancelable: true,
			Persistent: true,
			Retryable:  true,
		},
	}})

	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.GetManaged("running")
	if !ok {
		t.Fatal("running task missing after replay")
	}
	if got.Task.State != StateFailed || got.Task.Error == nil || got.Task.Error.Code != "interrupted" || got.Task.Capabilities.Cancelable {
		t.Fatalf("replayed running task = %+v", got.Task)
	}
}

func TestPersistentStoreJournalsOnlyDurableStateChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(ManagedTask{Task: Task{
		ID:    "batch",
		Type:  TypeUploadBatch,
		State: StateRunning,
		Capabilities: Capabilities{
			Persistent: true,
			Cancelable: true,
		},
	}})

	// Progress-only ticks must not journal.
	for range 100 {
		store.UpdateManaged("batch", func(mt *ManagedTask) {
			mt.Task.Progress.CloudBytesDone += 1 << 20
		})
	}
	// State transitions must journal.
	store.UpdateManaged("batch", func(mt *ManagedTask) {
		mt.Task.State = StatePartialFailed
		mt.Task.Progress.CloudBytesDone = 1 << 20
		mt.Task.Capabilities.Cancelable = false
		mt.Task.Capabilities.Retryable = true
		mt.Task.Capabilities.Dismissible = true
		mt.Task.Tracking.Items = []ItemTracking{{
			Path:  "/a.txt",
			State: ItemStateFailed,
			Error: &Error{Message: "boom"},
		}}
	})

	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.GetManaged("batch")
	if !ok {
		t.Fatal("task missing after replay")
	}
	if got.Task.State != StatePartialFailed || got.Task.Progress.CloudBytesDone != 1<<20 {
		t.Fatalf("replayed task = %+v, want partial_failed with final progress", got.Task)
	}
	if len(got.Task.Tracking.Items) != 1 || got.Task.Tracking.Items[0].Error == nil {
		t.Fatalf("replayed result = %+v, want failed item with error", got.Task.Tracking.Items)
	}
}

func TestPersistentStoreReportsStickyPersistenceFailure(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "tasks")
	path := filepath.Join(parent, "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("blocks directory creation"), 0o600); err != nil {
		t.Fatal(err)
	}

	item := store.PutManaged(ManagedTask{Task: Task{
		ID:    "persistent",
		State: StateRunning,
		Capabilities: Capabilities{
			Persistent: true,
		},
	}})
	if item.ID != "persistent" {
		t.Fatalf("in-memory task = %+v", item)
	}
	if err := store.PersistenceError(); !errors.Is(err, ErrPersistence) {
		t.Fatalf("PersistenceError() = %v, want ErrPersistence", err)
	}

	store.PutManaged(ManagedTask{Task: Task{ID: "memory-only"}})
	if err := store.PersistenceError(); !errors.Is(err, ErrPersistence) {
		t.Fatalf("PersistenceError() cleared after memory-only write: %v", err)
	}
}

func TestManagerReportsStorePersistenceFailure(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "tasks")
	store, err := NewPersistentStore(filepath.Join(parent, "tasks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("blocks directory creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerWithStore(store)
	manager.store.PutManaged(ManagedTask{Task: Task{
		ID:           "persistent",
		State:        StateRunning,
		Capabilities: Capabilities{Persistent: true},
	}})
	if err := manager.PersistenceError(); !errors.Is(err, ErrPersistence) {
		t.Fatalf("PersistenceError() = %v, want ErrPersistence", err)
	}
}

// Legacy journal entries carry no visibility declaration: replay infers it
// from the origin exactly once (user origin → visible, everything else →
// hidden). New creations declare visibility explicitly.
func TestReplayedTasksInferVisibilityFromOrigin(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []Task{
		{ID: "legacy-user", Type: TypeUploadRemote, State: StateSucceeded, Scope: ScopeUser, Capabilities: Capabilities{Persistent: true}},
		{ID: "legacy-sync", Type: TypeUploadRemote, State: StateSucceeded, Scope: ScopeSync, Capabilities: Capabilities{Persistent: true}},
		{ID: "legacy-internal", Type: TypeUploadRemote, State: StateSucceeded, Scope: ScopeInternal, Capabilities: Capabilities{Persistent: true}},
		{ID: "legacy-unspecified", Type: TypeUploadRemote, State: StateSucceeded, Capabilities: Capabilities{Persistent: true}},
		{ID: "declared-hidden-user", Type: TypeUploadRemote, State: StateSucceeded, Scope: ScopeUser, Visibility: VisibilityHidden, Capabilities: Capabilities{Persistent: true}},
	} {
		store.PutManaged(ManagedTask{Task: legacy})
	}
	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Journal-boundary decision: replay preserves the declared origin —
	// legacy sync records are never clobbered into internal (mount
	// bookkeeping never passed through this journal).
	for id, want := range map[string]struct {
		scope      Scope
		visibility Visibility
	}{
		"legacy-user":          {ScopeUser, VisibilityVisible},
		"legacy-sync":          {ScopeSync, VisibilityHidden},
		"legacy-internal":      {ScopeInternal, VisibilityHidden},
		"legacy-unspecified":   {"", VisibilityHidden},
		"declared-hidden-user": {ScopeUser, VisibilityHidden},
	} {
		managed, ok := reopened.GetManaged(id)
		if !ok {
			t.Fatalf("%s missing after replay", id)
		}
		if managed.Task.Scope != want.scope {
			t.Errorf("%s scope = %q, want %q preserved", id, managed.Task.Scope, want.scope)
		}
		if managed.Task.Visibility != want.visibility {
			t.Errorf("%s visibility = %q, want %q", id, managed.Task.Visibility, want.visibility)
		}
	}
}

// A legacy journal entry with a task-level handshake state or State
// vocabulary written into a phase label (both journaled by old code)
// normalizes at replay: the interrupted work fails with code "interrupted"
// and the phase labels become display wording (glossary: 阶段标签).
func TestReplayedLegacyHandshakeAndPhaseNormalizeOnReplay(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []Task{
		{ID: "waiting-input", Type: TypeUploadStreamBatch, State: legacyStateWaitingInput,
			Progress:     Progress{Phase: Phase("waiting_input")},
			Capabilities: Capabilities{Persistent: true}},
		{ID: "waiting-output", Type: TypeDownloadStreamBatch, State: legacyStateWaitingOutput,
			Progress:     Progress{Phase: Phase("waiting_output")},
			Capabilities: Capabilities{Persistent: true}},
		{ID: "retry-wait", Type: TypeUploadStreamDirect, State: StateRetryWait,
			Progress:     Progress{Phase: Phase("retry_wait")},
			Tracking:     Tracking{Items: []ItemTracking{{ItemID: "i", Phase: Phase("queued")}}},
			Capabilities: Capabilities{Persistent: true}},
	} {
		store.PutManaged(ManagedTask{Task: legacy})
	}
	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id        string
		wantPhase Phase
	}{
		{"waiting-input", PhaseAppStaging},
		{"waiting-output", PhaseReady},
		{"retry-wait", PhaseRetrying},
	} {
		managed, ok := reopened.GetManaged(tc.id)
		if !ok {
			t.Fatalf("%s missing after replay", tc.id)
		}
		if managed.Task.State != StateFailed || managed.Task.Error == nil || managed.Task.Error.Code != "interrupted" {
			t.Errorf("%s replayed = %+v, want failed interrupted", tc.id, managed.Task)
		}
		if managed.Task.Progress.Phase != tc.wantPhase {
			t.Errorf("%s phase = %q, want %q", tc.id, managed.Task.Progress.Phase, tc.wantPhase)
		}
	}
	managed, ok := reopened.GetManaged("retry-wait")
	if !ok {
		t.Fatal("retry-wait missing after replay")
	}
	if len(managed.Task.Tracking.Items) != 1 {
		t.Fatalf("replayed items = %+v, want one", managed.Task.Tracking.Items)
	}
	if got := managed.Task.Tracking.Items[0].Phase; got != PhasePending {
		t.Errorf("replayed item phase = %q, want pending", got)
	}
}
