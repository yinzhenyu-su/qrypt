package upload

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
)

// waitForState spins until cond holds. The condition is reached by service
// state changes the test triggers itself, so the deadline is only a hang
// guard; no fixed sleep decides the outcome.
func waitForState(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// TestScheduledDeadlineComesFromTheInjectedClock pins the clock seam: the
// debounce deadline is anchored to the injected clock's reading, exact to the
// nanosecond, which a wall-clock call could never be.
func TestScheduledDeadlineComesFromTheInjectedClock(t *testing.T) {
	store := newPendingStoreFixture(t)
	start := time.Now()
	clock := testclock.NewFake(start)
	const delay = time.Hour
	svc := NewService(ServiceOptions{Clock: clock, UploadDelay: delay, Store: store})
	defer svc.Close()

	pending := savePullRecord(t, store, "/clock-deadline.txt")
	svc.EnqueueAfter(pending, delay)

	deadline, ok := svc.ScheduledDeadlines()[pending.Path]
	if !ok {
		t.Fatal("path was not scheduled")
	}
	if want := start.Add(delay); !deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s: the injected clock did not anchor the debounce", deadline, want)
	}
}

// TestReceiveFiresWhenTheInjectedClockAdvances drives an otherwise hour-long
// debounce through the fake clock, so the test does not sleep at all.
func TestReceiveFiresWhenTheInjectedClockAdvances(t *testing.T) {
	store := newPendingStoreFixture(t)
	start := time.Now()
	clock := testclock.NewFake(start)
	const delay = 30 * time.Minute
	svc := NewService(ServiceOptions{Clock: clock, UploadDelay: delay, Store: store})
	defer svc.Close()

	pending := savePullRecord(t, store, "/clock-receive.txt")
	svc.EnqueueAfter(pending, delay)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan PendingUpload, 1)
	go func() {
		if p, ok := svc.Receive(ctx); ok {
			received <- p
		}
	}()

	waitForState(t, "the receive timer to be armed on the fake clock", func() bool { return clock.Pending() > 0 })
	select {
	case p := <-received:
		t.Fatalf("received %q before the fake clock advanced", p.Path)
	default:
	}
	// Cross the debounce with margin: the claim also re-checks the quiet
	// window, which is anchored to the store's AppliedAt stamp (a few
	// microseconds after the fake clock was created).
	clock.Advance(delay + time.Minute)
	select {
	case p := <-received:
		if p.Path != pending.Path {
			t.Fatalf("received %q, want %q", p.Path, pending.Path)
		}
		svc.CompleteReceive(p.Path)
	case <-time.After(5 * time.Second):
		t.Fatal("receive did not fire after the fake clock advanced")
	}
}

// TestWaitIdleBlocksUntilTheJournalIsEmpty checks the idle primitive itself:
// it reports idle only after the claim is completed and the durable record is
// gone, and it is woken by those transitions rather than by polling.
func TestWaitIdleBlocksUntilTheJournalIsEmpty(t *testing.T) {
	store := newPendingStoreFixture(t)
	clock := testclock.NewFake(time.Now())
	svc := NewService(ServiceOptions{Clock: clock, UploadDelay: time.Hour, Store: store})
	defer svc.Close()

	pending := savePullRecord(t, store, "/wait-idle.txt")
	svc.EnqueueAfter(pending, time.Hour)
	if svc.Idle() {
		t.Fatal("service claims idle while a debounce is scheduled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- svc.WaitIdle(ctx) }()
	select {
	case err := <-waited:
		t.Fatalf("WaitIdle returned %v before the upload drained", err)
	default:
	}

	uploaded := make(chan struct{})
	go func() {
		defer close(uploaded)
		p, ok := svc.Receive(ctx)
		if !ok {
			return
		}
		if err := svc.RemoveUpload(p.Path); err != nil {
			t.Errorf("remove upload: %v", err)
		}
		svc.CompleteReceive(p.Path)
	}()
	// The margin covers the quiet window's AppliedAt anchor, which is stamped
	// a few microseconds after the fake clock was created.
	clock.Advance(time.Hour + time.Minute)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("WaitIdle = %v, want nil after the journal drained", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return after the upload drained")
	}
	<-uploaded
	if !svc.Idle() {
		t.Fatal("service is not idle after draining")
	}
}

// TestWaitIdleReportsClosedServiceWithPendingWork keeps WaitIdle honest for a
// service that can never make progress again.
func TestWaitIdleReportsClosedServiceWithPendingWork(t *testing.T) {
	store := newPendingStoreFixture(t)
	svc := NewService(ServiceOptions{UploadDelay: time.Hour, Store: store})
	pending := savePullRecord(t, store, "/closed-idle.txt")
	svc.EnqueueAfter(pending, time.Hour)
	svc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.WaitIdle(ctx); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("WaitIdle = %v, want ErrServiceClosed", err)
	}
}
