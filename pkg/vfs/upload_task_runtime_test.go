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
