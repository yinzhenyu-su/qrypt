package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTimer mimics a started-but-not-yet-run timer callback. fire runs
// the callback synchronously regardless of Stop, which is exactly the
// "callback already dispatched" window of time.AfterFunc.
type fakeTimer struct {
	fn      func()
	stopped bool
}

func (t *fakeTimer) fire() {
	if t.fn != nil {
		t.fn()
	}
}

func (t *fakeTimer) Stop() bool {
	t.stopped = true
	return true
}

// fakeTimerFactory hands out controllable timers; tests fire them
// explicitly, so no real time and no sleeps are needed.
type fakeTimerFactory struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (f *fakeTimerFactory) all() []*fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeTimer(nil), f.timers...)
}

// newTestScheduler builds a scheduler whose timers are controllable
// fakes: Stop is recorded, firing is manual, and a fired callback runs
// synchronously - exactly the "callback already dispatched" window.
type testScheduler struct {
	*timeKeyedScheduler
	factory *fakeTimerFactory
}

func newTestScheduler() *testScheduler {
	factory := &fakeTimerFactory{}
	ts := &testScheduler{factory: factory}
	ts.timeKeyedScheduler = newScheduler(factory.timerFactory)
	return ts
}

func (f *fakeTimerFactory) timerFactory(_ time.Duration, fn func()) timerHandle {
	t := &fakeTimer{fn: fn}
	f.mu.Lock()
	f.timers = append(f.timers, t)
	f.mu.Unlock()
	return t
}

// TestReplaceBeforeFire: replacing while the old timer is still pending
// stops the old callback entirely; firing it manually after the replace
// must be a stale no-op (generation check), leaving the new task intact.
func TestReplaceBeforeFire(t *testing.T) {
	s := newTestScheduler()
	var oldRan, newRan atomic.Bool
	s.Schedule("A", 0, func() { oldRan.Store(true) })
	s.Schedule("A", 0, func() { newRan.Store(true) })

	timers := s.factory.all()
	if len(timers) != 2 {
		t.Fatalf("timers = %d, want 2", len(timers))
	}
	// The stale timer was stopped by the replace; firing it anyway (the
	// time.AfterFunc race window) must not run the old callback.
	timers[0].fire()
	if oldRan.Load() {
		t.Fatal("replaced callback ran")
	}
	if _, ok := s.Keys()["A"]; !ok {
		t.Fatal("stale callback deleted new task state")
	}
	// Fire the new timer: the replacement runs exactly once.
	timers[1].fire()
	if !newRan.Load() {
		t.Fatal("replacement callback never ran")
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys after fire = %+v", got)
	}
}

// TestStaleCallbackCannotCorruptNewState: the review-reported race, made
// deterministic: the old timer fires AFTER the replace has already won.
func TestStaleCallbackCannotCorruptNewState(t *testing.T) {
	s := newTestScheduler()
	var staleRan, newRan atomic.Bool
	s.Schedule("A", 0, func() { staleRan.Store(true) })
	s.Schedule("A", 0, func() { newRan.Store(true) })

	timers := s.factory.all()
	// Deterministic stale window: old timer fires after replace.
	timers[0].fire()
	if staleRan.Load() {
		t.Fatal("stale callback ran")
	}
	if _, ok := s.Keys()["A"]; !ok {
		t.Fatal("stale callback deleted new task state")
	}
	if _, ok := s.Deadlines()["A"]; !ok {
		t.Fatal("stale callback deleted new deadline")
	}
	timers[1].fire()
	if !newRan.Load() {
		t.Fatal("replacement never fired")
	}
}

// TestCancelAfterFireLeavesNothing: a fired-then-cancelled callback is a
// stale no-op and leaves no state.
func TestCancelAfterFireLeavesNothing(t *testing.T) {
	s := newTestScheduler()
	var ran atomic.Bool
	s.Schedule("A", 0, func() { ran.Store(true) })
	timers := s.factory.all()
	s.Cancel("A")
	timers[0].fire() // stale: cancelled before running
	if ran.Load() {
		t.Fatal("cancelled callback ran")
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys after cancel = %+v", got)
	}
}

// TestCancelAllAfterFireLeavesNothing: same for CancelAll across keys.
func TestCancelAllAfterFireLeavesNothing(t *testing.T) {
	s := newTestScheduler()
	var a, b atomic.Bool
	s.Schedule("A", 0, func() { a.Store(true) })
	s.Schedule("B", 0, func() { b.Store(true) })
	timers := s.factory.all()
	s.CancelAll()
	for _, t := range timers {
		t.fire()
	}
	if a.Load() || b.Load() {
		t.Fatalf("cancelled callbacks ran a=%v b=%v", a.Load(), b.Load())
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys after cancel-all = %+v", got)
	}
}

// TestScheduleFiresOnce: a normally firing timer runs its callback once
// and clears its own state.
func TestScheduleFiresOnce(t *testing.T) {
	s := newTestScheduler()
	var calls atomic.Int64
	s.Schedule("A", 0, func() { calls.Add(1) })
	s.factory.all()[0].fire()
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys after fire = %+v", got)
	}
}

// TestCancelUnderRemovesSubtree: CancelUnder removes the exact key and
// all paths under it; other keys keep their callbacks.
func TestCancelUnderRemovesSubtree(t *testing.T) {
	s := newTestScheduler()
	var under, other atomic.Int64
	s.Schedule("/dir", 0, func() { under.Add(1) })
	s.Schedule("/dir/a.txt", 0, func() { under.Add(1) })
	s.Schedule("/dir/sub/b.txt", 0, func() { under.Add(1) })
	s.Schedule("/other.txt", 0, func() { other.Add(1) })
	timers := s.factory.all()
	s.CancelUnder("/dir")
	for _, t := range timers {
		t.fire()
	}
	if got := under.Load(); got != 0 {
		t.Fatalf("subtree callbacks ran %d times", got)
	}
	if got := other.Load(); got != 1 {
		t.Fatalf("other callback ran %d times, want 1", got)
	}
}

// TestConcurrentScheduleCancel: racing Schedule/Cancel/Keys/Deadlines must
// be race-free and converge to a consistent final state.
func TestConcurrentScheduleCancel(t *testing.T) {
	s := NewTimeKeyedScheduler()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var calls atomic.Int64

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.Schedule("A", time.Millisecond, func() { calls.Add(1) })
		}
		close(stop)
	}()
	wg.Add(3)
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.Cancel("A")
				s.Keys()
				s.Deadlines()
			}
		}()
	}
	wg.Wait()

	// The last Schedule may still be pending; allow 0 or 1 fires.
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got > 1 {
		t.Fatalf("callback ran %d times, want at most 1", got)
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys = %+v, want empty after fire", got)
	}
}
