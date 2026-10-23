package upload

import (
	"context"
	"testing"
	"time"
)

func TestServiceProcessPendingExecutesCurrentGeneration(t *testing.T) {
	t.Parallel()
	store := newPendingStoreFixture(t)
	pending := PendingUpload{
		Path:      "/file.txt",
		FID:       "current",
		LocalPath: store.StagingPath("current"),
		Frozen:    true,
		UpdatedAt: time.Now().Add(-time.Hour).UnixNano(),
	}
	if err := store.SaveUploadExact(pending); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{Store: store, UploadWorkers: 1})
	executed := 0
	err := svc.ProcessPending(context.Background(), pending, ProcessOptions{
		SourceUploadSupported: true,
		Execute: func(context.Context, PendingUpload) error {
			executed++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
}

func TestServiceProcessPendingRequeuesSupersedingGeneration(t *testing.T) {
	t.Parallel()
	store := newPendingStoreFixture(t)
	old := PendingUpload{Path: "/file.txt", FID: "old", LocalPath: store.StagingPath("old"), Frozen: true}
	latest := PendingUpload{Path: "/file.txt", FID: "new", LocalPath: store.StagingPath("new"), Frozen: true}
	if err := store.SaveUploadExact(latest); err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{Store: store, UploadWorkers: 1})
	if err := svc.ProcessPending(context.Background(), old, ProcessOptions{
		SourceUploadSupported: true,
		SchedulingEnabled:     func() bool { return true },
		Execute: func(context.Context, PendingUpload) error {
			t.Fatal("superseded generation executed")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.ScheduledDeadlines()[latest.Path]; !ok {
		t.Fatal("superseding generation was not scheduled")
	}
}

func TestServiceProcessPendingWaitsForQuietWindow(t *testing.T) {
	t.Parallel()
	store := newPendingStoreFixture(t)
	pending := PendingUpload{
		Path:      "/file.txt",
		FID:       "current",
		LocalPath: store.StagingPath("current"),
		Frozen:    true,
		UpdatedAt: time.Now().UnixNano(),
	}
	if err := store.SaveUploadExact(pending); err != nil {
		t.Fatal(err)
	}
	pending, _ = store.UploadByPath(pending.Path)
	svc := NewService(ServiceOptions{Store: store, UploadDelay: time.Hour, UploadWorkers: 1})
	executed := false
	if err := svc.ProcessPending(context.Background(), pending, ProcessOptions{
		SourceUploadSupported: true,
		SchedulingEnabled:     func() bool { return true },
		Execute: func(context.Context, PendingUpload) error {
			executed = true
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("quiet generation executed")
	}
	if _, ok := svc.ScheduledDeadlines()[pending.Path]; !ok {
		t.Fatal("quiet generation was not rescheduled")
	}
}

func TestServiceProcessPendingWaitsForAdmission(t *testing.T) {
	t.Parallel()
	store := newPendingStoreFixture(t)
	pending := PendingUpload{
		Path:      "/file.txt",
		FID:       "current",
		LocalPath: store.StagingPath("current"),
		Frozen:    true,
		UpdatedAt: time.Now().Add(-time.Hour).UnixNano(),
	}
	if err := store.SaveUploadExact(pending); err != nil {
		t.Fatal(err)
	}
	pending, _ = store.UploadByPath(pending.Path)
	svc := NewService(ServiceOptions{Store: store, UploadWorkers: 1})
	if !svc.TryAcquire(PendingUpload{}, 1) {
		t.Fatal("failed to occupy upload admission")
	}
	defer svc.Release(PendingUpload{})
	if err := svc.ProcessPending(context.Background(), pending, ProcessOptions{
		SourceUploadSupported: true,
		SchedulingEnabled:     func() bool { return true },
		Execute: func(context.Context, PendingUpload) error {
			t.Fatal("generation executed without admission")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	deadline, ok := svc.ScheduledDeadlines()[pending.Path]
	if !ok {
		t.Fatal("generation waiting for admission was not rescheduled")
	}
	if delay := time.Until(deadline); delay > 250*time.Millisecond {
		t.Fatalf("admission retry delay = %s, want <= 250ms", delay)
	}
}
