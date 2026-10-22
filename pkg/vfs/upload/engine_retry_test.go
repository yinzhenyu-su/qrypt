package upload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type retryRecordingRuntime struct {
	fakeCommitRuntime
	requeued []PendingUpload
}

func (r *retryRecordingRuntime) Requeue(p PendingUpload) {
	r.requeued = append(r.requeued, p)
}

func (r *retryRecordingRuntime) RetryDelay(int) time.Duration { return time.Second }

// TestExecuteSchedulesRetryWhenRemotePreparationFails pins that a transient
// failure before PutSource (here the parent List) is persisted with a retry
// deadline instead of leaving a frozen pending record unscheduled forever.
func TestExecuteSchedulesRetryWhenRemotePreparationFails(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.StagingDir(), "up.staging")
	if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:      "/up.txt",
		FID:       "fid-up",
		ParentID:  "parent",
		Name:      "up.txt",
		LocalPath: staging,
		Size:      4,
		Frozen:    true,
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, _ := store.UploadByPath(pending.Path)

	runtime := &retryRecordingRuntime{}
	engine := NewEngine(EngineDeps{
		Remote:        &targetIndexRemote{listErr: errors.New("transient list failure")},
		Observer:      &fakeCommitObserver{},
		Pending:       NewStoreAdapter(store),
		Runtime:       runtime,
		View:          &recordingView{},
		Invalidations: &recordingInvalidations{store: store},
		Snapshot:      &fakeCommitSnapshot{},
		Faults:        &fakeCommitFaults{},
	})
	if err := engine.Execute(context.Background(), current); err == nil {
		t.Fatal("Execute succeeded despite the List failure")
	}
	if len(runtime.requeued) != 1 {
		t.Fatalf("requeued = %d, want 1", len(runtime.requeued))
	}
	record, ok := store.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending record disappeared after a retryable failure")
	}
	if record.PermanentFail || record.LastError == "" || record.NextAttemptAt == 0 {
		t.Fatalf("failure state = %+v, want retryable failure with next attempt", record)
	}
}

// TestRecordFailureTreatsAuthAsPermanent pins the shared error-category
// classification: drivers commonly wrap ErrAuth/ErrInvalidInput without
// drive.NonRetryable, and those must not be retried forever.
func TestRecordFailureTreatsAuthAsPermanent(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.StagingDir(), "up.staging")
	if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:      "/up.txt",
		FID:       "fid-up",
		ParentID:  "parent",
		Name:      "up.txt",
		LocalPath: staging,
		Size:      4,
		Frozen:    true,
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, _ := store.UploadByPath(pending.Path)

	runtime := &retryRecordingRuntime{}
	engine := NewEngine(EngineDeps{Pending: NewStoreAdapter(store), Runtime: runtime})
	if ok := engine.recordFailure(current, fmt.Errorf("%w: token expired", drive.ErrAuth)); !ok {
		t.Fatal("recordFailure did not record the permanent failure")
	}
	if len(runtime.requeued) != 0 {
		t.Fatalf("auth failure was requeued %d times, want 0", len(runtime.requeued))
	}
	record, ok := store.UploadByPath(pending.Path)
	if !ok || !record.PermanentFail {
		t.Fatalf("record = %+v ok=%v, want permanent failure", record, ok)
	}
}
