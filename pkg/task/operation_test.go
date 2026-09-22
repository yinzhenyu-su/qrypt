package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOperationRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		request OperationRequest
		wantErr error
	}{
		{
			name: "valid upload",
			request: OperationRequest{
				Operation:    OperationUpload,
				Items:        []Item{{DestPath: "/file.txt"}},
				UploadPolicy: UploadPolicyPreferDirect,
			},
		},
		{
			name:    "missing operation",
			request: OperationRequest{Items: []Item{{Path: "/file.txt"}}},
			wantErr: ErrInvalidOperation,
		},
		{
			name:    "missing items",
			request: OperationRequest{Operation: OperationUpload},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "upload policy on non-upload",
			request: OperationRequest{
				Operation:    OperationDelete,
				Items:        []Item{{Path: "/file.txt"}},
				UploadPolicy: UploadPolicyStagingOnly,
			},
			wantErr: ErrInvalidOperation,
		},
		{
			name: "invalid upload policy",
			request: OperationRequest{
				Operation:    OperationUpload,
				Items:        []Item{{DestPath: "/file.txt"}},
				UploadPolicy: UploadPolicy("unknown"),
			},
			wantErr: ErrInvalidOperation,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestManagerSubmitIdempotentReturnsExistingTask(t *testing.T) {
	m := NewManager()
	defer m.Close()

	first, err := m.SubmitIdempotent(context.Background(), Task{
		ID:                   "first",
		Scope:                ScopeUser,
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-1",
	}, func(context.Context, UpdateFunc) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.SubmitIdempotent(context.Background(), Task{
		ID:                   "second",
		Scope:                ScopeUser,
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-1",
	}, func(context.Context, UpdateFunc) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("second task id = %q, want existing id %q", second.ID, first.ID)
	}
}

func TestManagerSubmitIdempotentRejectsFingerprintConflict(t *testing.T) {
	m := NewManager()
	defer m.Close()

	_, err := m.SubmitIdempotent(context.Background(), Task{
		ID:                   "first",
		Scope:                ScopeUser,
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-1",
	}, func(context.Context, UpdateFunc) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.SubmitIdempotent(context.Background(), Task{
		ID:                   "second",
		Scope:                ScopeUser,
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-2",
	}, func(context.Context, UpdateFunc) error { return nil })
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("SubmitIdempotent() error = %v, want %v", err, ErrIdempotencyConflict)
	}
}

func TestPersistentManagerRetainsIdempotencyRecord(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "tasks.jsonl")
	store, err := NewPersistentStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	firstManager := NewManagerWithStore(store)
	first, err := firstManager.SubmitIdempotent(context.Background(), Task{
		ID:                   "first",
		Scope:                ScopeUser,
		Capabilities:         Capabilities{Persistent: true},
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-1",
	}, func(ctx context.Context, _ UpdateFunc) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		firstManager.Close()
		t.Fatal(err)
	}
	firstManager.Close()

	reopened, err := NewPersistentStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	secondManager := NewManagerWithStore(reopened)
	defer secondManager.Close()
	second, err := secondManager.SubmitIdempotent(context.Background(), Task{
		ID:                   "second",
		Scope:                ScopeUser,
		Capabilities:         Capabilities{Persistent: true},
		IdempotencyKey:       "request-1",
		OperationFingerprint: "fingerprint-1",
	}, func(context.Context, UpdateFunc) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("reopened task id = %q, want %q", second.ID, first.ID)
	}
}

func TestManagerIgnoresStaleGenerationUpdate(t *testing.T) {
	m := NewManager()
	started := make(chan struct{})
	if _, err := m.SubmitIdempotent(context.Background(), Task{ID: "task-1"}, func(ctx context.Context, _ UpdateFunc) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	managed, ok := m.store.GetManaged("task-1")
	if !ok {
		m.Close()
		t.Fatal("task not stored")
	}
	oldGeneration := managed.Task.ExecutionGeneration
	m.store.UpdateManaged("task-1", func(managed *ManagedTask) {
		managed.Task.ExecutionGeneration++
	})
	m.updateForGeneration("task-1", oldGeneration, func(item *Task) {
		item.Detail = map[string]any{"stale": true}
	})
	current, ok := m.store.GetManaged("task-1")
	m.Close()
	if !ok {
		t.Fatal("task disappeared")
	}
	if current.Task.Detail["stale"] != nil {
		t.Fatalf("stale update was applied: %+v", current.Task.Detail)
	}
}
