package readbudget

import (
	"context"
	"testing"
	"time"
)

func TestRangeAdmissionDoesNotHoldPartialSlot(t *testing.T) {
	s := NewScheduler(SchedulerConfig{MaxConcurrency: 2, PrefetchLimit: 1})
	hold, err := s.AcquireRange(context.Background(), "a", 1, PriorityNormal)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, err := s.AcquireRange(ctx, "a", 1, PriorityNormal)
		if err == nil {
			release()
		}
		done <- err
	}()
	// Waiting for file a must not consume the only global slot. File b can run.
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		queued := len(s.waiting) > 0
		s.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter never queued")
		}
		time.Sleep(time.Millisecond)
	}
	release, ok := s.TryAcquireRange("b", 1, PriorityLow)
	if !ok {
		t.Fatal("waiter held a partial slot")
	}
	release()
	release()
	if got := s.Stats().NormalInUse; got != 1 {
		t.Fatalf("release not idempotent: %d", got)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled waiter succeeded")
	}
	if s.PerFileInUse("a") != 1 {
		t.Fatal("waiter changed held file permit")
	}
}

func TestRangeTryNeverRetainsPartialPermit(t *testing.T) {
	s := NewScheduler(SchedulerConfig{MaxConcurrency: 1, PrefetchLimit: 1})
	hold, _ := s.AcquireRange(context.Background(), "a", 1, PriorityNormal)
	if release, ok := s.TryAcquireRange("b", 1, PriorityLow); ok {
		release()
		t.Fatal("exceeded global capacity")
	}
	if s.PerFileInUse("b") != 0 {
		t.Fatal("failed try retained file permit")
	}
	hold()
	release, ok := s.TryAcquireRange("b", 1, PriorityLow)
	if !ok {
		t.Fatal("released capacity not available")
	}
	release()
}
