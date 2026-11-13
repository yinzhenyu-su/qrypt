package upload

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type retryRecordingRuntime struct {
	fakeCommitRuntime
	requeued     []PendingUpload
	requeueDelay []time.Duration
}

func (r *retryRecordingRuntime) RequeueAfter(p PendingUpload, delay time.Duration) {
	r.requeued = append(r.requeued, p)
	r.requeueDelay = append(r.requeueDelay, delay)
}

func (r *retryRecordingRuntime) RetryDelay(retryCount int) time.Duration {
	return time.Duration(retryCount) * time.Second
}

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
	// The retry must be scheduled with the backoff measured from the requeue,
	// not from record.NextAttemptAt: that bound is anchored at the failed
	// attempt, so a journal append slower than the backoff leaves it in the
	// past and scheduling from it would retry immediately.
	if len(runtime.requeueDelay) != 1 || runtime.requeueDelay[0] != runtime.RetryDelay(1) {
		t.Fatalf("requeue delays = %v, want [%s]", runtime.requeueDelay, runtime.RetryDelay(1))
	}
}

// TestRecordFailureIgnoresStaleDeadlineWhenRequeueing pins that a failure whose
// persisted NextAttemptAt is already in the past (the failure became durable
// only after its backoff) is still requeued with a full backoff.
func TestRecordFailureIgnoresStaleDeadlineWhenRequeueing(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.StagingDir(), "up.staging")
	if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:          "/up.txt",
		FID:           "fid-up",
		ParentID:      "parent",
		Name:          "up.txt",
		LocalPath:     staging,
		Size:          4,
		Frozen:        true,
		LastError:     "transient network failure",
		NextAttemptAt: time.Now().Add(-time.Minute).UnixNano(),
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, _ := store.UploadByPath(pending.Path)

	runtime := &retryRecordingRuntime{}
	engine := NewEngine(EngineDeps{Pending: NewStoreAdapter(store), Runtime: runtime})
	if ok := engine.recordFailure(current, errors.New("transient network failure")); !ok {
		t.Fatal("recordFailure did not record the failure")
	}
	if len(runtime.requeueDelay) != 1 || runtime.requeueDelay[0] != runtime.RetryDelay(current.RetryCount+1) {
		t.Fatalf("requeue delays = %v, want [%s]", runtime.requeueDelay, runtime.RetryDelay(current.RetryCount+1))
	}
}

func TestRecordFailureTreatsNotFoundAsPermanent(t *testing.T) {
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
	// SFTP's staging OpenFile surfaces a missing parent directory as
	// os.ErrNotExist; a deleted target must not be retried forever.
	err = fmt.Errorf("sftp: upload part 0: open remote staging file: %w", fs.ErrNotExist)
	if ok := engine.recordFailure(current, err); !ok {
		t.Fatal("recordFailure did not record the not-found failure")
	}
	if len(runtime.requeued) != 0 {
		t.Fatalf("not-found failure was requeued %d times, want 0", len(runtime.requeued))
	}
	record, ok := store.UploadByPath(pending.Path)
	if !ok || !record.PermanentFail {
		t.Fatalf("record = %+v ok=%v, want permanent failure", record, ok)
	}
}

func TestRecordFailureStopsAfterRetryLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		retryCount  int
		wantRequeue bool
	}{
		{name: "below limit", retryCount: maxUploadRetries - 1, wantRequeue: true},
		{name: "at limit", retryCount: maxUploadRetries, wantRequeue: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewPendingStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			staging := filepath.Join(store.StagingDir(), "up.staging")
			if err := os.WriteFile(staging, []byte("data"), 0o644); err != nil {
				t.Fatal(err)
			}
			pending := PendingUpload{
				Path:       "/up.txt",
				FID:        "fid-up",
				ParentID:   "parent",
				Name:       "up.txt",
				LocalPath:  staging,
				Size:       4,
				RetryCount: tc.retryCount,
				Frozen:     true,
			}
			if err := store.SaveUpload(pending); err != nil {
				t.Fatal(err)
			}
			current, _ := store.UploadByPath(pending.Path)

			runtime := &retryRecordingRuntime{}
			engine := NewEngine(EngineDeps{Pending: NewStoreAdapter(store), Runtime: runtime})
			if ok := engine.recordFailure(current, errors.New("transient network failure")); !ok {
				t.Fatal("recordFailure did not record the failure")
			}
			if got := len(runtime.requeued); (got == 1) != tc.wantRequeue {
				t.Fatalf("requeued = %d, want requeue=%v", got, tc.wantRequeue)
			}
			record, ok := store.UploadByPath(pending.Path)
			if !ok {
				t.Fatal("pending record disappeared")
			}
			if record.PermanentFail != !tc.wantRequeue {
				t.Fatalf("PermanentFail = %v, want %v", record.PermanentFail, !tc.wantRequeue)
			}
		})
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
