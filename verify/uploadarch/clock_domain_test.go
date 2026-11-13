package uploadarch

import (
	"errors"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

// scheduleProbe measures, for one clock configuration, when the scheduler
// decides to act on (a) a file that was just written and (b) an upload that just
// failed with a retryable error. The upload debounce is 3s in both cases.
type scheduleProbe struct {
	debounceAfter time.Duration // scheduled delay for the just-written file
	backoffAfter  time.Duration // scheduled delay for the failed upload
	recordedBound time.Duration // time.Until(NextAttemptAt), read with the system clock
}

func probeSchedule(t *testing.T, skew time.Duration) scheduleProbe {
	t.Helper()
	reset := setClockSkew(t, skew)
	defer reset()

	store, err := upload.NewPendingStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const debounce = 3 * time.Second
	svc := upload.NewService(upload.ServiceOptions{
		UploadDelay: debounce,
		Store:       store,
		Done:        make(chan struct{}),
	})
	defer svc.Close()

	// A file the write path just created: UpdatedAt is stamped with util.Now(),
	// which is what createLocked/WriteAt do in production.
	written := stagedPending(t, store, "/written.txt")
	written.UpdatedAt = util.Now().UnixNano()
	if err := store.SaveUpload(written); err != nil {
		t.Fatal(err)
	}
	current, ok := store.UploadByPath(written.Path)
	if !ok {
		t.Fatal("written record missing")
	}
	svc.Enqueue(current)
	debounceAfter := scheduledAfter(t, svc, written.Path)

	// The same file after one retryable failure: the store computes the retry
	// deadline with util.Now(), the scheduler compares it with the system clock.
	failed := stagedPending(t, store, "/failed.txt")
	failed.UpdatedAt = util.Now().UnixNano()
	if err := store.SaveUpload(failed); err != nil {
		t.Fatal(err)
	}
	current, ok = store.UploadByPath(failed.Path)
	if !ok {
		t.Fatal("failed record missing")
	}
	next, ok, err := store.RecordUploadFailureIfUnchanged(current, errors.New("transient network failure"), debounce)
	if err != nil || !ok {
		t.Fatalf("record failure: ok=%v err=%v", ok, err)
	}
	recordedBound := time.Until(time.Unix(0, next.NextAttemptAt))
	svc.Enqueue(next)
	backoffAfter := scheduledAfter(t, svc, failed.Path)

	return scheduleProbe{
		debounceAfter: debounceAfter,
		backoffAfter:  backoffAfter,
		recordedBound: recordedBound,
	}
}

func stagedPending(t *testing.T, store *upload.PendingStore, path string) upload.PendingUpload {
	t.Helper()
	local, err := store.CreateStaging(path)
	if err != nil {
		t.Fatal(err)
	}
	return upload.PendingUpload{
		Path:      path,
		FID:       path,
		ParentID:  "0",
		Name:      path,
		LocalPath: local,
		Size:      4,
		Frozen:    true,
	}
}

func scheduledAfter(t *testing.T, svc *upload.Service, path string) time.Duration {
	t.Helper()
	deadline, ok := svc.ScheduledDeadlines()[path]
	if !ok {
		t.Fatalf("upload %q was not scheduled", path)
	}
	return time.Until(deadline)
}

func ms(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// TestClockDomainsMeetInTheScheduler measures what a non-zero NTP offset does to
// the two scheduling decisions. The store stamps deadlines with util.Now()
// (system clock plus the NTP offset); the scheduler measures the remaining time
// with the system clock. A local clock that is 5s fast therefore looks like 5s
// of silence and 5s of elapsed backoff.
func TestClockDomainsMeetInTheScheduler(t *testing.T) {
	// No t.Parallel: NTP state is process-global.
	skewed := probeSchedule(t, -5*time.Second)
	honest := probeSchedule(t, 0)

	t.Logf("system clock 5s fast (NTP offset -5s): debounce scheduled after %s, retry scheduled after %s, persisted retry bound reads %s",
		ms(skewed.debounceAfter), ms(skewed.backoffAfter), ms(skewed.recordedBound))
	t.Logf("system clock in sync:                  debounce scheduled after %s, retry scheduled after %s, persisted retry bound reads %s",
		ms(honest.debounceAfter), ms(honest.backoffAfter), ms(honest.recordedBound))

	// The honest clock is the control: both decisions honor the 3s delay.
	if honest.debounceAfter < 2*time.Second {
		t.Fatalf("control debounce scheduled after %s, want ~3s", ms(honest.debounceAfter))
	}
	if honest.backoffAfter < 2*time.Second {
		t.Fatalf("control retry scheduled after %s, want ~3s", ms(honest.backoffAfter))
	}
	if honest.recordedBound <= 0 {
		t.Fatalf("control retry bound reads %s, want positive", ms(honest.recordedBound))
	}

	// The skewed clock turns both delays into no-ops.
	if skewed.debounceAfter > time.Second {
		t.Fatalf("debounce with a skewed clock scheduled after %s, want ~0", ms(skewed.debounceAfter))
	}
	if skewed.backoffAfter > time.Second {
		t.Fatalf("retry with a skewed clock scheduled after %s, want ~0", ms(skewed.backoffAfter))
	}
	if skewed.recordedBound > 0 {
		t.Fatalf("persisted retry bound with a skewed clock reads %s, want negative", ms(skewed.recordedBound))
	}
}
