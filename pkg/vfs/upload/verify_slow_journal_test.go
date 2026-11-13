package upload

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestVerifySlowAppendStaleness reproduces, without a slow disk, how a journal
// append that outlives its own deadline changes scheduling. The engine's
// previous requeue path is modelled with Service.Enqueue (schedules from the
// record alone); the current path is modelled with Service.EnqueueAfter(p, backoff).
//
// Three configurations are measured:
//
//	A  test-scale debounce (the CI test config: upload_delay=10ms)
//	B  the debounce template default (upload_delay=3s) with a 500ms append
//	C  the same default with a 4s fsync stall
func TestVerifySlowAppendStaleness(t *testing.T) {
	if testing.Short() {
		t.Skip("models multi-second journal stalls")
	}
	backoff := RetryDelay(1, 0)
	t.Logf("first retry backoff = %s", backoff)

	for _, tc := range []struct {
		name       string
		debounce   time.Duration
		appendCost time.Duration
	}{
		{name: "A test-scale debounce", debounce: 10 * time.Millisecond, appendCost: 500 * time.Millisecond},
		{name: "B template debounce", debounce: 3 * time.Second, appendCost: 500 * time.Millisecond},
		{name: "C template debounce, stalled fsync", debounce: 3 * time.Second, appendCost: 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewPendingStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			localPath := filepath.Join(store.StagingDir(), "slow.staging")
			if err := os.WriteFile(localPath, []byte("data"), 0o644); err != nil {
				t.Fatal(err)
			}
			pending := PendingUpload{
				Path:      "/slow.txt",
				FID:       "slow",
				ParentID:  "0",
				Name:      "slow.txt",
				LocalPath: localPath,
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

			store.afterJournalSync = func() { time.Sleep(tc.appendCost) }
			start := time.Now()
			next, ok, err := store.RecordUploadFailureIfUnchanged(current, errors.New("transient network failure"), backoff)
			elapsed := time.Since(start)
			if err != nil || !ok {
				t.Fatalf("record failure: ok=%v err=%v", ok, err)
			}
			remaining := time.Until(time.Unix(0, next.NextAttemptAt))

			svc := NewService(ServiceOptions{
				UploadDelay: tc.debounce,
				Store:       store,
				Done:        make(chan struct{}),
			})
			defer svc.Close()

			// Pre-fix path: requeue from the record (what Requeue(latest) did).
			svc.Enqueue(next)
			preFix := scheduledAfter(t, svc, next.Path)
			// Current path: requeue with the backoff measured from now.
			svc.EnqueueAfter(next, backoff)
			current2 := scheduledAfter(t, svc, next.Path)

			t.Logf("append=%s debounce=%s -> recorded bound %s; old requeue scheduled %s; current requeue scheduled %s",
				elapsed.Round(time.Millisecond), tc.debounce, remaining.Round(time.Millisecond),
				preFix.Round(time.Millisecond), current2.Round(time.Millisecond))

			if tc.appendCost > backoff {
				if remaining > 0 {
					t.Fatalf("recorded bound is %s in the future, want it already due after a %s append", remaining, tc.appendCost)
				}
			}
			// Old path: the whole delay (debounce or backoff, whichever is larger)
			// is measured from before the append, so the append is subtracted.
			expectedOld := max(tc.debounce, backoff) - elapsed
			if expectedOld < 0 {
				expectedOld = 0
			}
			if diff := preFix - expectedOld; diff < -150*time.Millisecond || diff > 150*time.Millisecond {
				t.Fatalf("old requeue scheduled %s, want ~%s (max(debounce, backoff) - append)", preFix.Round(time.Millisecond), expectedOld.Round(time.Millisecond))
			}
			// Current path: the backoff is no longer eroded, but the debounce still
			// is, because it is measured from the record's pre-append UpdatedAt.
			if current2 < backoff-100*time.Millisecond {
				t.Fatalf("current requeue scheduled %s, want at least the %s backoff", current2.Round(time.Millisecond), backoff)
			}
		})
	}
}

func scheduledAfter(t *testing.T, svc *Service, path string) time.Duration {
	t.Helper()
	deadline, ok := svc.ScheduledDeadlines()[path]
	if !ok {
		t.Fatalf("upload %q was not scheduled", path)
	}
	return time.Until(deadline)
}
