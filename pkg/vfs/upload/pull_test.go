package upload

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func savePullRecord(t *testing.T, store *PendingStore, path string) PendingUpload {
	t.Helper()
	local, err := store.CreateStaging(strings.ReplaceAll(path[1:], "/", "_"))
	if err != nil {
		t.Fatal(err)
	}
	p := PendingUpload{Path: path, FID: path, LocalPath: local, Frozen: true, UpdatedAt: time.Now().Add(-time.Hour).UnixNano()}
	if err := store.SaveUploadExact(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPullBacklogSurvivesRestartAndDrainsBeyondInboxCapacity(t *testing.T) {
	store := newPendingStoreFixture(t)
	svc := NewService(ServiceOptions{Store: store, UploadDelay: time.Millisecond})
	defer svc.Close()
	const count = 300
	for i := range count {
		p := savePullRecord(t, store, fmt.Sprintf("/file-%d", i))
		svc.EnqueueAfter(p, 0)
	}
	if len(svc.Queue()) != 0 || len(svc.deadlines) != count {
		t.Fatal("backlog was pushed into the compatibility inbox or lost")
	}
	// Discard all in-memory scheduling state and recover from disk.
	svc.Close()
	reopened, err := NewPendingStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered := NewService(ServiceOptions{Store: reopened, UploadDelay: time.Millisecond})
	defer recovered.Close()
	for _, p := range reopened.PendingUploads() {
		recovered.EnqueueAfter(p, 0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(map[string]bool)
	for range count {
		p, ok := recovered.Receive(ctx)
		if !ok || seen[p.Path] {
			t.Fatalf("missing or duplicate recovered record: %+v", p)
		}
		seen[p.Path] = true
		recovered.CompleteReceive(p.Path)
	}
}

func TestPullCoalescesTenThousandUpdatesAndExcludesClaimedPath(t *testing.T) {
	store := newPendingStoreFixture(t)
	svc := NewService(ServiceOptions{Store: store, UploadDelay: time.Millisecond})
	defer svc.Close()
	a := savePullRecord(t, store, "/a")
	b := savePullRecord(t, store, "/b")
	for range 10_000 {
		svc.EnqueueAfter(a, 0)
	}
	if len(svc.deadlines) != 1 || len(svc.scheduled) != 1 {
		t.Fatal("same-path updates accumulated instead of replacing the deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, ok := svc.Receive(ctx)
	if !ok || first.Path != a.Path {
		t.Fatal("first path not claimed")
	}
	// A new generation must wait for the old claim, while other paths run.
	a.FID = "new-generation"
	if err := store.SaveUploadExact(a); err != nil {
		t.Fatal(err)
	}
	svc.EnqueueAfter(a, 0)
	svc.EnqueueAfter(b, 0)
	second, ok := svc.Receive(ctx)
	if !ok || second.Path != b.Path {
		t.Fatal("claimed path was received concurrently")
	}
	svc.CompleteReceive(first.Path)
	latest, ok := svc.Receive(ctx)
	if !ok || latest.FID != a.FID {
		t.Fatalf("new generation not received: %+v", latest)
	}
	svc.CompleteReceive(latest.Path)
	svc.CompleteReceive(second.Path)
}

func TestPullTenThousandPathsDoNotSpawnWaitingGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	svc := NewService(ServiceOptions{Store: newPendingStoreFixture(t)})
	defer svc.Close()
	for i := range 10_000 {
		path := fmt.Sprintf("/bulk-%d", i)
		svc.EnqueueAfter(PendingUpload{Path: path, FID: path}, 0)
	}
	if len(svc.deadlines) != 10_000 || len(svc.Queue()) != 0 {
		t.Fatal("bulk scheduling lost paths or filled a content inbox")
	}
}

func TestPullRechecksJournalQuietRetryAndFrozenState(t *testing.T) {
	for _, state := range []string{"writing", "quiet", "retry", "permanent"} {
		t.Run(state, func(t *testing.T) {
			store := newPendingStoreFixture(t)
			svc := NewService(ServiceOptions{Store: store, UploadDelay: time.Hour})
			defer svc.Close()
			p := savePullRecord(t, store, "/a")
			svc.EnqueueAfter(p, 0)
			switch state {
			case "writing":
				p.Frozen = false
			case "quiet":
				p.UpdatedAt = time.Now().UnixNano()
			case "retry":
				p.NextAttemptAt = time.Now().Add(time.Hour).UnixNano()
			case "permanent":
				p.PermanentFail = true
			}
			if err := store.SaveUploadExact(p); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if _, ok := svc.Receive(ctx); ok {
				t.Fatal("ineligible current journal record was received")
			}
		})
	}
}

func TestQuietWindowTemporaryNamesAndFinalRename(t *testing.T) {
	svc := NewService(ServiceOptions{UploadDelay: 30 * time.Second})
	defer svc.Close()
	for _, path := range []string{"/movie.qkdownloading", "/movie.BC!", "/._movie.qkdownloading"} {
		if got := svc.QuietWindow(PendingUpload{Path: path}); got != 5*time.Minute {
			t.Fatalf("%s quiet window = %s", path, got)
		}
	}
	if got := svc.QuietWindow(PendingUpload{Path: "/movie.mp4"}); got != 30*time.Second {
		t.Fatalf("final name quiet window = %s", got)
	}
	if got := svc.QuietWindow(PendingUpload{Path: "/movie.mp4", Size: LargeUploadQuietThreshold}); got != time.Minute {
		t.Fatalf("large final name quiet window = %s", got)
	}
}

func TestPullCancelAndCloseWakeIdleWorkers(t *testing.T) {
	store := newPendingStoreFixture(t)
	svc := NewService(ServiceOptions{Store: store})
	p := savePullRecord(t, store, "/dir/a")
	svc.EnqueueAfter(p, time.Hour)
	svc.CancelChildUploads("/dir")
	if len(svc.deadlines) != 0 {
		t.Fatal("cancelled deadline remains in heap")
	}
	finished := make(chan bool, 1)
	go func() {
		_, ok := svc.Receive(context.Background())
		finished <- ok
	}()
	svc.Close()
	select {
	case ok := <-finished:
		if ok {
			t.Fatal("closed service returned a record")
		}
	case <-time.After(time.Second):
		t.Fatal("idle worker did not exit")
	}
}
