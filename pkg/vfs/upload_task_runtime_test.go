package vfs

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

type fakeUploadTaskRuntime struct {
	records        []uploadTaskRecord
	pending        map[string]PendingUpload
	active         map[string]string
	states         map[string]string
	cancelRemoved  []string
	retried        []string
	removedHistory []string
}

func (r *fakeUploadTaskRuntime) Records() []uploadTaskRecord {
	return append([]uploadTaskRecord(nil), r.records...)
}

func (r *fakeUploadTaskRuntime) PendingByID(id string) (PendingUpload, bool) {
	pending, ok := r.pending[id]
	return pending, ok
}

func (r *fakeUploadTaskRuntime) ActivePathByID(id string) (string, bool) {
	path, ok := r.active[id]
	return path, ok
}

func (r *fakeUploadTaskRuntime) StateByID(id string) (string, bool) {
	if state, ok := r.states[id]; ok {
		return state, true
	}
	for _, record := range r.records {
		if record.id == id {
			return record.state, true
		}
	}
	return "", false
}

func (r *fakeUploadTaskRuntime) CancelAndRemove(path string) error {
	r.cancelRemoved = append(r.cancelRemoved, path)
	return nil
}

func (r *fakeUploadTaskRuntime) Retry(pending PendingUpload) error {
	r.retried = append(r.retried, pending.Path)
	return nil
}

func (r *fakeUploadTaskRuntime) RemoveHistoryByID(id string) bool {
	r.removedHistory = append(r.removedHistory, id)
	return true
}

func TestUploadTaskSourceUsesRuntimeForCancelRetryAndDismiss(t *testing.T) {
	runtime := &fakeUploadTaskRuntime{
		pending: map[string]PendingUpload{
			"pending-id": {FID: "pending-id", Path: "/pending.txt"},
			"retry-id":   {FID: "retry-id", Path: "/retry.txt"},
		},
		active: map[string]string{"active-id": "/active.txt"},
	}
	source := uploadTaskSource{runtime: runtime}
	if err := source.Cancel(context.Background(), "pending-id"); err != nil {
		t.Fatal(err)
	}
	if err := source.Retry(context.Background(), "retry-id"); err != nil {
		t.Fatal(err)
	}
	if err := source.Dismiss(context.Background(), "active-id"); err != nil {
		t.Fatal(err)
	}
	if err := source.Dismiss(context.Background(), "history-id"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.cancelRemoved; len(got) != 2 || got[0] != "/pending.txt" || got[1] != "/active.txt" {
		t.Fatalf("cancelRemoved = %+v", got)
	}
	if got := runtime.retried; len(got) != 1 || got[0] != "/retry.txt" {
		t.Fatalf("retried = %+v", got)
	}
	if got := runtime.removedHistory; len(got) != 1 || got[0] != "history-id" {
		t.Fatalf("removedHistory = %+v", got)
	}
}

func TestUploadTaskSourceDismissDoesNotCancelSucceededActiveUpload(t *testing.T) {
	runtime := &fakeUploadTaskRuntime{
		pending: map[string]PendingUpload{
			"succeeded-active-id": {FID: "succeeded-active-id", Path: "/done.txt"},
		},
		active: map[string]string{"succeeded-active-id": "/done.txt"},
		states: map[string]string{"succeeded-active-id": string(drive.UploadPhaseCompleted)},
	}
	source := uploadTaskSource{runtime: runtime}
	if err := source.Dismiss(context.Background(), "succeeded-active-id"); err != nil {
		t.Fatal(err)
	}
	// A succeeded upload that is still active (engine finishing the commit)
	// must not be canceled: cancel-and-remove would drop the pending record
	// and the engine would delete the freshly uploaded remote file.
	if got := runtime.cancelRemoved; len(got) != 0 {
		t.Fatalf("cancelRemoved = %+v, want none", got)
	}
	if len(runtime.pending) != 1 {
		t.Fatalf("pending = %+v, want preserved", runtime.pending)
	}
}

func TestUploadTaskSourceDismissFinishedUsesRuntimeHistory(t *testing.T) {
	now := time.Now()
	runtime := &fakeUploadTaskRuntime{
		records: []uploadTaskRecord{
			{id: "done", path: "/done.txt", name: "done.txt", state: string(drive.UploadPhaseCompleted), updatedAt: now, completedAt: now},
			{id: "running", path: "/running.txt", name: "running.txt", state: string(drive.UploadPhaseUploading), updatedAt: now},
		},
		pending: map[string]PendingUpload{},
		active:  map[string]string{},
	}
	source := uploadTaskSource{runtime: runtime}
	removed, err := source.DismissFinished(context.Background(), task.Filter{Types: []task.Type{task.TypeUploadRemote}})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := runtime.removedHistory; len(got) != 1 || got[0] != "done" {
		t.Fatalf("removedHistory = %+v", got)
	}
}
