package task

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestBoundIdempotencyKeysSurviveReplay pins alias-key binding: an active task
// can collect several explicit keys, every key conflicts on different content,
// and replay restores them from the task journal.
func TestBoundIdempotencyKeysSurviveReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks", "tasks.jsonl")
	store, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.PutManaged(ManagedTask{Task: Task{
		ID:                   "offline-1",
		Type:                 TypeOfflineDownload,
		Operation:            OperationDownload,
		Scope:                ScopeUser,
		State:                StateSucceeded,
		IdempotencyKey:       "key-1",
		OperationFingerprint: "content-a",
		Capabilities:         Capabilities{Persistent: true},
		Detail:               map[string]any{},
	}})
	if _, ok := store.UpdateManaged("offline-1", func(managed *ManagedTask) {
		if !BindIdempotencyKeyFingerprint(&managed.Task, "key-2", "content-a") {
			t.Error("binding a secondary key was rejected")
		}
		if !BindIdempotencyKeyFingerprint(&managed.Task, "key-3", "content-c") {
			t.Error("binding a third key was rejected")
		}
	}); !ok {
		t.Fatal("task missing while binding keys")
	}

	reopened, err := NewPersistentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, ok := reopened.GetManaged("offline-1")
	if !ok {
		t.Fatal("task missing after replay")
	}
	if !HasIdempotencyKey(replayed.Task, "key-1") || !HasIdempotencyKey(replayed.Task, "key-2") || !HasIdempotencyKey(replayed.Task, "key-3") {
		t.Fatalf("replayed keys = %v / %v, want the primary and both bound keys", replayed.Task.IdempotencyKey, BoundIdempotencyKeys(replayed.Task))
	}
	if replayed.Task.IdempotencyKey != "key-1" {
		t.Fatalf("primary key = %q, want key-1 (binding must not replace it)", replayed.Task.IdempotencyKey)
	}

	manager := NewManagerWithStore(reopened)
	defer manager.Close()
	found, ok, err := manager.FindIdempotent(Task{
		Type:                 TypeOfflineDownload,
		Scope:                ScopeUser,
		IdempotencyKey:       "key-2",
		OperationFingerprint: "content-a",
	})
	if err != nil || !ok || found.ID != "offline-1" {
		t.Fatalf("replayed key lookup = %+v ok=%v err=%v", found, ok, err)
	}
	if _, _, err := manager.FindIdempotent(Task{
		Type:                 TypeOfflineDownload,
		Scope:                ScopeUser,
		IdempotencyKey:       "key-2",
		OperationFingerprint: "content-b",
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("bound key with different content error = %v, want idempotency conflict", err)
	}
	found, ok, err = manager.FindIdempotent(Task{
		Type:                 TypeOfflineDownload,
		Scope:                ScopeUser,
		IdempotencyKey:       "key-3",
		OperationFingerprint: "content-c",
	})
	if err != nil || !ok || found.ID != "offline-1" {
		t.Fatalf("second alias lookup = %+v ok=%v err=%v", found, ok, err)
	}
}
