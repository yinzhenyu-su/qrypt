package uploadarch

import (
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

// TestSchedulerTrustsPersistedBoundNotPolicy checks the scheduling input: the
// service never consults the retry policy, it only reads the persisted
// NextAttemptAt. Hand it the state a slow journal append leaves behind (a bound
// that is already due) and the retry is booked immediately even though this
// attempt's backoff is seconds long.
func TestSchedulerTrustsPersistedBoundNotPolicy(t *testing.T) {
	store, err := upload.NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pending := stagedPending(t, store, "/retry.txt")
	pending.RetryCount = 4
	pending.LastError = "transient network failure"
	// The quiet window has long expired, so only the retry bound can delay it.
	pending.UpdatedAt = time.Now().Add(-time.Hour).UnixNano()
	// What RecordUploadFailureIfUnchanged leaves when its journal append took
	// longer than the backoff: a deadline that has already passed.
	pending.NextAttemptAt = time.Now().Add(time.Millisecond).UnixNano()
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}

	svc := upload.NewService(upload.ServiceOptions{
		UploadDelay: 3 * time.Second,
		Store:       store,
		Done:        make(chan struct{}),
	})
	defer svc.Close()
	svc.Enqueue(pending)

	got := scheduledAfter(t, svc, pending.Path)
	policy := upload.RetryDelay(pending.RetryCount, 3*time.Second)
	t.Logf("retry #%d with a persisted bound 1ms ahead: scheduled after %s; the policy backoff is %s",
		pending.RetryCount, ms(got), ms(policy))

	if got > time.Second {
		t.Fatalf("scheduler waited %s; expected it to trust the stale bound and act immediately", ms(got))
	}
	if policy < 3*time.Second {
		t.Fatalf("policy backoff = %s, want seconds (sanity check on RetryDelay)", ms(policy))
	}
}

// TestRecordFailureAppendLatency measures the window that can make the retry
// bound stale: a failure is only schedulable after its journal append (write +
// fsync) returns, and the bound was computed before that. The first retry
// backoff is 375ms, so an append slower than that leaves the bound in the past.
func TestRecordFailureAppendLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("measures real journal fsync latency")
	}
	store, err := upload.NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pending := stagedPending(t, store, "/latency.txt")
	if err := store.SaveUpload(pending); err != nil {
		t.Fatal(err)
	}
	current, ok := store.UploadByPath(pending.Path)
	if !ok {
		t.Fatal("pending record missing")
	}

	const rounds = 50
	latencies := make([]time.Duration, 0, rounds)
	for i := 0; i < rounds; i++ {
		start := time.Now()
		next, ok, err := store.RecordUploadFailureIfUnchanged(current, errors.New("transient network failure"), 375*time.Millisecond)
		latencies = append(latencies, time.Since(start))
		if err != nil || !ok {
			t.Fatalf("round %d: ok=%v err=%v", i, ok, err)
		}
		current = next
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	firstBackoff := upload.RetryDelay(1, 0)
	t.Logf("journal append+fsync for one failure record: p50=%s p95=%s max=%s; first retry backoff=%s",
		ms(latencies[len(latencies)/2]),
		ms(latencies[len(latencies)*95/100]),
		ms(latencies[len(latencies)-1]),
		ms(firstBackoff))
	if latencies[len(latencies)-1] > firstBackoff {
		t.Logf("observed an append longer than the first backoff: the recorded bound can already be due by the time the failure is visible")
		return
	}
	t.Logf("healthy local disk: appends stay far below the first backoff; the windows runner logged ~600ms between the failure and its requeue")
}
