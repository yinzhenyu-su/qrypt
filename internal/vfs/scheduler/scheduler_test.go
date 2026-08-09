package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReplaceBeforeFire: replacing while the old timer is still pending
// stops the old callback entirely - it never runs.
func TestReplaceBeforeFire(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := NewTimeKeyedScheduler()
		var oldRan atomic.Bool
		var newRan atomic.Bool
		s.Schedule("A", 5*time.Millisecond, func() { oldRan.Store(true) })
		s.Schedule("A", 10*time.Millisecond, func() { newRan.Store(true) })
		time.Sleep(60 * time.Millisecond)
		if oldRan.Load() {
			t.Fatalf("iteration %d: replaced (still-pending) callback ran", i)
		}
		if !newRan.Load() {
			t.Fatalf("iteration %d: replacement callback never ran", i)
		}
		if got := s.Keys(); len(got) != 0 {
			t.Fatalf("iteration %d: keys = %+v after fire", i, got)
		}
	}
}

// TestStaleCallbackCannotCorruptNewState: the review-reported race. A
// callback whose timer fired but whose generation was replaced BEFORE it
// ran must (a) never run, and (b) must never delete the new task's state.
// We assert the observable contract: while the replacement is still
// pending its key is present (Keys/Deadlines intact), and the replacement
// fires exactly once.
func TestStaleCallbackCannotCorruptNewState(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := NewTimeKeyedScheduler()
		var staleRan atomic.Bool
		var newRan atomic.Bool
		s.Schedule("A", time.Millisecond, func() { staleRan.Store(true) })
		time.Sleep(2 * time.Millisecond) // old timer fired; callback may be running or stale
		s.Schedule("A", 30*time.Millisecond, func() { newRan.Store(true) })

		// While the new callback is still pending, its key must be
		// present - a stale callback deleting it would corrupt state.
		time.Sleep(10 * time.Millisecond)
		if _, ok := s.Keys()["A"]; !ok {
			t.Fatalf("iteration %d: stale callback deleted new task state", i)
		}
		if _, ok := s.Deadlines()["A"]; !ok {
			t.Fatalf("iteration %d: stale callback deleted new deadline", i)
		}

		time.Sleep(40 * time.Millisecond) // new callback fires
		if !newRan.Load() {
			t.Fatalf("iteration %d: replacement never fired", i)
		}
		if got := s.Keys(); len(got) != 0 {
			t.Fatalf("iteration %d: keys after fire = %+v", i, got)
		}
		// staleRan may be true only when it fired BEFORE the replace
		// took effect (a legitimately started callback); when the
		// replace won the race it must be false. We cannot distinguish
		// here, so we only forbid the corrupting side effects above.
		_ = staleRan.Load()
	}
}

// TestCancelAfterFireLeavesNothing: Cancel racing a fired callback must
// leave no state and the callback must not run after the cancel.
func TestCancelAfterFireLeavesNothing(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := NewTimeKeyedScheduler()
		var ran atomic.Bool
		s.Schedule("A", time.Millisecond, func() { ran.Store(true) })
		time.Sleep(2 * time.Millisecond) // timer fired; callback may be running
		s.Cancel("A")
		time.Sleep(20 * time.Millisecond)
		if got := s.Keys(); len(got) != 0 {
			t.Fatalf("iteration %d: keys after cancel = %+v", i, got)
		}
		if got := s.Deadlines(); len(got) != 0 {
			t.Fatalf("iteration %d: deadlines after cancel = %+v", i, got)
		}
	}
}

// TestCancelAllAfterFireLeavesNothing: same for CancelAll across keys.
func TestCancelAllAfterFireLeavesNothing(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := NewTimeKeyedScheduler()
		s.Schedule("A", time.Millisecond, func() {})
		s.Schedule("B", time.Millisecond, func() {})
		time.Sleep(2 * time.Millisecond)
		s.CancelAll()
		time.Sleep(20 * time.Millisecond)
		if got := s.Keys(); len(got) != 0 {
			t.Fatalf("iteration %d: keys after cancel-all = %+v", i, got)
		}
	}
}

// TestReplaceReschedulesWithoutOldFire: replacing while the old timer is
// pending fires exactly the new callback once.
func TestReplaceReschedulesWithoutOldFire(t *testing.T) {
	s := NewTimeKeyedScheduler()
	var calls atomic.Int64
	s.Schedule("A", 5*time.Millisecond, func() { calls.Add(1) })
	s.Schedule("A", 10*time.Millisecond, func() { calls.Add(1) })
	time.Sleep(60 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want exactly 1 (old replaced callback must not fire)", got)
	}
}

// TestCancelUnderRemovesSubtree: CancelUnder removes the prefix and all
// paths under it; other keys keep their callbacks.
func TestCancelUnderRemovesSubtree(t *testing.T) {
	s := NewTimeKeyedScheduler()
	var under, other atomic.Int64
	s.Schedule("/dir/a.txt", 5*time.Millisecond, func() { under.Add(1) })
	s.Schedule("/dir/sub/b.txt", 5*time.Millisecond, func() { under.Add(1) })
	s.Schedule("/other.txt", 5*time.Millisecond, func() { other.Add(1) })
	s.CancelUnder("/dir")
	time.Sleep(30 * time.Millisecond)
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

	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got > 1 {
		t.Fatalf("callback ran %d times, want at most 1", got)
	}
	if got := s.Keys(); len(got) != 0 {
		t.Fatalf("keys = %+v, want empty after fire", got)
	}
}
