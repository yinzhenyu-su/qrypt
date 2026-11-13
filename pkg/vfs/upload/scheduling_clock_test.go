package upload

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// newScheduledRecord stores one frozen pending record and returns it with a
// staging file, so scheduling can be measured without an upload engine.
func newScheduledRecord(t *testing.T, store *PendingStore, path string) PendingUpload {
	t.Helper()
	local := filepath.Join(store.StagingDir(), "scheduled.staging")
	if err := os.WriteFile(local, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending := PendingUpload{
		Path:      path,
		FID:       "fid-" + path,
		ParentID:  "0",
		Name:      path,
		LocalPath: local,
		Size:      4,
		Frozen:    true,
	}
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, ok := store.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending record missing")
	}
	return current
}

func scheduledIn(t *testing.T, svc *Service, path string) time.Duration {
	t.Helper()
	deadline, ok := svc.ScheduledDeadlines()[path]
	if !ok {
		t.Fatalf("upload %q was not scheduled", path)
	}
	return time.Until(deadline)
}

// TestQuietDelayAnchorsAfterTheJournalAppend pins that the debounce is measured
// from the moment the record became current, not from a timestamp taken before
// the journal append: a slow append must not eat the quiet window.
func TestQuietDelayAnchorsAfterTheJournalAppend(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const window = 500 * time.Millisecond
	const appendCost = 300 * time.Millisecond
	var appends int
	store.afterJournalSync = func() {
		appends++
		time.Sleep(appendCost)
	}

	pending := PendingUpload{
		Path:      "/slow-write.txt",
		FID:       "slow-write",
		ParentID:  "0",
		Name:      "slow-write.txt",
		LocalPath: filepath.Join(store.StagingDir(), "slow-write.staging"),
		Size:      4,
		Frozen:    true,
	}
	if err := os.WriteFile(pending.LocalPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < appendCost {
		t.Fatalf("append was not slowed down: %s", elapsed)
	}
	current, ok := store.UploadByPath(pending.Path)
	if !ok || appends == 0 {
		t.Fatalf("record missing or append hook not called: ok=%v appends=%d", ok, appends)
	}

	svc := NewService(ServiceOptions{UploadDelay: window, Store: store, Done: make(chan struct{})})
	defer svc.Close()
	got := svc.QuietDelay(current)
	t.Logf("after a %s journal append, a %s quiet window has %s left", appendCost, window, got.Round(time.Millisecond))
	if got < window-100*time.Millisecond {
		t.Fatalf("quiet delay = %s, want ~%s: the append must not be subtracted from the window", got, window)
	}
}

// TestRetryWaitDerivesFromAttemptFacts pins that the retry wait comes from the
// persisted attempt facts (count and time), not from the persisted deadline: a
// deadline left in the past by a slow append cannot shorten the backoff.
func TestRetryWaitDerivesFromAttemptFacts(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// No debounce, so only the retry wait can delay the upload.
	svc := NewService(ServiceOptions{Store: store, Done: make(chan struct{})})
	defer svc.Close()

	const retryCount = 4
	const sinceLastAttempt = time.Second
	pending := newScheduledRecord(t, store, "/retry.txt")
	pending.RetryCount = retryCount
	pending.LastError = "transient network failure"
	pending.LastAttemptAt = util.Now().Add(-sinceLastAttempt).UnixNano()
	// The state a journal append slower than the backoff leaves behind.
	pending.NextAttemptAt = util.Now().Add(-time.Minute).UnixNano()
	pending.UpdatedAt = util.Now().Add(-time.Minute).UnixNano()
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, _ := store.UploadByPath(pending.Path)

	svc.Enqueue(current)
	got := scheduledIn(t, svc, pending.Path)
	want := RetryDelay(retryCount, 0) - sinceLastAttempt
	t.Logf("retry #%d with a stale deadline scheduled after %s, want ~%s", retryCount, got.Round(time.Millisecond), want.Round(time.Millisecond))
	if diff := got - want; diff < -200*time.Millisecond || diff > 200*time.Millisecond {
		t.Fatalf("retry scheduled after %s, want ~%s (derived from attempt facts)", got, want)
	}

	// An explicit user retry is a grant on the record: it bypasses the wait.
	granted, ok := store.RunNow(current)
	if !ok {
		t.Fatal("run-now grant rejected")
	}
	svc.Enqueue(granted)
	if got := scheduledIn(t, svc, pending.Path); got > time.Second {
		t.Fatalf("granted retry scheduled after %s, want it to run now", got)
	}
}

// TestRetryRunsNowWithoutBackdatingUpdatedAt pins the explicit-retry path: the
// upload starts immediately (even for a five-minute temporary-name window) while
// the persisted timestamps stay honest, and the grant is dropped by the next
// state change so normal scheduling comes back.
func TestRetryRunsNowWithoutBackdatingUpdatedAt(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{UploadDelay: time.Minute, Store: store, Done: make(chan struct{})})
	defer svc.Close()

	pending := newScheduledRecord(t, store, "/download.mp4.qkdownloading")
	svc.Enqueue(pending)
	if got := scheduledIn(t, svc, pending.Path); got < 4*time.Minute {
		t.Fatalf("temporary-name debounce = %s, want the five-minute window", got)
	}

	if err := svc.Retry(pending); err != nil {
		t.Fatal(err)
	}
	retried, _ := store.UploadByPath(pending.Path)
	if age := time.Since(time.Unix(0, retried.UpdatedAt)); age < 0 || age > 10*time.Second {
		t.Fatalf("UpdatedAt age = %s, want a real recent timestamp (no backdating)", age)
	}
	if got := scheduledIn(t, svc, pending.Path); got > time.Second {
		t.Fatalf("retry scheduled after %s, want it to run now", got)
	}

	// A new state change drops the grant: the record is back on the normal floor.
	retried.Size++
	store.UpdateUploadTransient(retried)
	refreshed, _ := store.UploadByPath(pending.Path)
	if refreshed.RunNow {
		t.Fatal("run-now grant survived a state change")
	}
	if got := svc.QuietDelay(refreshed); got < 4*time.Minute {
		t.Fatalf("quiet delay after the reset = %s, want the five-minute window back", got)
	}
}

// TestQuietDelayFallsBackToPersistedStamp covers records recovered from the
// journal: they carry no in-process anchor, so the window is derived from the
// persisted stamp, which both sides read from the same wall clock.
func TestQuietDelayFallsBackToPersistedStamp(t *testing.T) {
	store, err := NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(ServiceOptions{UploadDelay: time.Second, Store: store, Done: make(chan struct{})})
	defer svc.Close()

	recovered := PendingUpload{Path: "/recovered.txt", Size: 4, UpdatedAt: util.Now().Add(-400 * time.Millisecond).UnixNano()}
	if got := svc.QuietDelay(recovered); got < 500*time.Millisecond || got > 700*time.Millisecond {
		t.Fatalf("quiet delay of a recovered record = %s, want ~600ms", got)
	}
	recovered.UpdatedAt = util.Now().Add(-2 * time.Second).UnixNano()
	if got := svc.QuietDelay(recovered); got != 0 {
		t.Fatalf("quiet delay of an old recovered record = %s, want 0", got)
	}
}
