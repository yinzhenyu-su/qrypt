package task

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
		Result: Result{Items: []ItemResult{{
			Path:  "/missing.txt",
			State: StateFailed,
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
	if got.Task.State != StatePartialFailed || len(got.Task.Result.Items) != 1 || got.Task.Result.Items[0].Error == nil {
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
	for i := 0; i < 100; i++ {
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
		mt.Task.Result.Items = []ItemResult{{
			Path:  "/a.txt",
			State: StateFailed,
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
	if len(got.Task.Result.Items) != 1 || got.Task.Result.Items[0].Error == nil {
		t.Fatalf("replayed result = %+v, want failed item with error", got.Task.Result.Items)
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
