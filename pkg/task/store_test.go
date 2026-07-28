package task

import "testing"

func TestMemoryStoreReturnsTaskSnapshots(t *testing.T) {
	store := NewMemoryStore()
	item := store.PutManaged(ManagedTask{
		Task: Task{
			ID:     "task-1",
			Type:   TypeDeleteBatch,
			State:  StateRunning,
			Detail: map[string]any{"phase": "delete"},
			Result: Result{
				Items: []ItemResult{{
					Path:  "/missing.txt",
					State: StateFailed,
					Error: &Error{Message: "missing"},
				}},
			},
		},
	})
	item.Detail["phase"] = "mutated"
	item.Result.Items[0].Error.Message = "mutated"

	got, ok := store.GetManaged("task-1")
	if !ok {
		t.Fatal("task missing")
	}
	if got.Task.Detail["phase"] != "delete" || got.Task.Result.Items[0].Error.Message != "missing" {
		t.Fatalf("stored task was mutated: %+v", got.Task)
	}

	got.Task.Detail["phase"] = "mutated again"
	got.Task.Result.Items[0].Error.Message = "mutated again"
	again, ok := store.GetManaged("task-1")
	if !ok {
		t.Fatal("task missing after get")
	}
	if again.Task.Detail["phase"] != "delete" || again.Task.Result.Items[0].Error.Message != "missing" {
		t.Fatalf("returned task shares storage: %+v", again.Task)
	}
}

func TestMemoryStoreUpdateIncrementsVersion(t *testing.T) {
	store := NewMemoryStore()
	first := store.PutManaged(ManagedTask{Task: Task{ID: "task-1", Type: TypeDeleteBatch}})
	updated, ok := store.UpdateManaged("task-1", func(managed *ManagedTask) {
		managed.Task.State = StateRunning
	})
	if !ok {
		t.Fatal("task missing")
	}
	if updated.Task.Version <= first.Version {
		t.Fatalf("version did not increase: first=%d updated=%d", first.Version, updated.Task.Version)
	}
	if updated.Task.State != StateRunning {
		t.Fatalf("updated task = %+v, want running", updated.Task)
	}
}

func TestMemoryStoreDismissManaged(t *testing.T) {
	store := NewMemoryStore()
	store.PutManaged(ManagedTask{Task: Task{ID: "task-1", Type: TypeDeleteBatch}})
	if !store.DismissManaged("task-1") {
		t.Fatal("DismissManaged returned false")
	}
	if _, ok := store.GetManaged("task-1"); ok {
		t.Fatal("task still exists after dismiss")
	}
	if store.DismissManaged("task-1") {
		t.Fatal("DismissManaged returned true for missing task")
	}
}
