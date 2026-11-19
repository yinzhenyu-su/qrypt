package read

import (
	"context"
	"net/http"
	"testing"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// fakeClockForTest returns a fake clock at a fixed instant.
func fakeClockForTest() *testclock.Fake {
	return testclock.NewFake(time.Unix(1_700_000_000, 0))
}

// pumpFakeClock advances clk while a timer is pending, so a test can let code
// under test block on a backoff and have it fire immediately. The caller closes
// done once the operation returns; the helper then exits. The step budget
// bounds the loop: stale timers (for example an idle watch whose body already
// finished) stay pending in the fake clock and must not be allowed to fire.
func pumpFakeClock(clk *testclock.Fake, step time.Duration, maxSteps int, done <-chan struct{}) {
	for i := 0; i < maxSteps; i++ {
		select {
		case <-done:
			return
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(step)
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// TestReadStatusRetryUsesInjectedClock proves the read path threads the drive
// clock seam into its retry backoff: the 200ms status backoff elapses on the
// fake clock, so a replayed read finishes in microseconds of real time while
// still paying a full backoff of virtual time.
func TestReadStatusRetryUsesInjectedClock(t *testing.T) {
	clk := fakeClockForTest()
	backend := &scriptedReadBackend{failures: []error{providerStatus(t, http.StatusInternalServerError)}}
	ctx := drive.WithClock(
		WithFetchTimeouts(context.Background(), drive.FetchTimeouts{Idle: 5 * time.Second, Total: 10 * time.Second}),
		clk,
	)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go pumpFakeClock(clk, 10*time.Millisecond, 400, done)
	defer close(done)

	started := time.Now()
	startedFake := clk.Now()
	data, extra, err := testReadRange(ctx, rangeEntry(), 0, 0, backend)
	if err != nil {
		t.Fatalf("read failed despite a transient 500: %v", err)
	}
	if len(data) != ChunkSize {
		t.Fatalf("bytes = %d, want %d", len(data), ChunkSize)
	}
	if backend.calls != 2 {
		t.Fatalf("driver calls = %d, want the failed attempt plus one replay", backend.calls)
	}
	if got := extra["fetch_attempts"]; got != 2 {
		t.Fatalf("fetch_attempts = %v, want 2", got)
	}
	if advanced := clk.Now().Sub(startedFake); advanced < 200*time.Millisecond*3/4 {
		t.Fatalf("fake clock advanced %s, want at least the ~%s status backoff", advanced, 200*time.Millisecond)
	}
	if wall := time.Since(started); wall > 500*time.Millisecond {
		t.Fatalf("read took %s of real time; the retry must run on the injected clock", wall)
	}
}
