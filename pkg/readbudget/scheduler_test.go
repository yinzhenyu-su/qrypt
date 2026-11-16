package readbudget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCapacityChangedBroadcastAndReleaseOnce(t *testing.T) {
	s := NewScheduler(SchedulerConfig{MaxConcurrency: 1, PrefetchLimit: 1})
	before := s.CapacityChanged()
	release, err := s.AcquireRange(context.Background(), "file", 1, PriorityNormal)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-before:
		t.Fatal("acquisition reported released capacity")
	default:
	}
	release()
	select {
	case <-before:
	default:
		t.Fatal("range release did not broadcast")
	}
	next := s.CapacityChanged()
	release()
	select {
	case <-next:
		t.Fatal("duplicate release notified twice")
	default:
	}
	if !s.TryAcquirePrefetch() {
		t.Fatal("cannot acquire prefetch")
	}
	s.ReleasePrefetch()
	select {
	case <-next:
	default:
		t.Fatal("prefetch release did not broadcast")
	}
}

func TestSchedulerSlotPriority(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 2, HighReserve: 1, PrefetchLimit: 1})
	ctx := context.Background()

	normal, err := acquireTestSlot(scheduler, ctx, PriorityNormal)
	if err != nil {
		t.Fatalf("normal acquire: %v", err)
	}
	high, err := acquireTestSlot(scheduler, ctx, PriorityHigh)
	if err != nil {
		t.Fatalf("high acquire: %v", err)
	}
	if stats := scheduler.Stats(); stats.NormalInUse != 1 || stats.HighInUse != 1 {
		t.Fatalf("slots = normal %d high %d, want 1/1", stats.NormalInUse, stats.HighInUse)
	}
	normal()
	high()
	if stats := scheduler.Stats(); stats.NormalInUse != 0 || stats.HighInUse != 0 {
		t.Fatalf("slots after release = normal %d high %d, want 0/0", stats.NormalInUse, stats.HighInUse)
	}
}

// A high-priority request that finds both pools full must not wait on the
// reserve alone: a normal slot released while it waits has to wake it, or it
// blocks until its context expires with a slot sitting idle.
func TestSchedulerHighPriorityWakesOnReleasedNormalSlot(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 2, HighReserve: 1, PrefetchLimit: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	normal, err := acquireTestSlot(scheduler, ctx, PriorityNormal)
	if err != nil {
		t.Fatalf("normal acquire: %v", err)
	}
	high, err := acquireTestSlot(scheduler, ctx, PriorityHigh)
	if err != nil {
		t.Fatalf("high acquire: %v", err)
	}
	defer high()

	acquired := make(chan func(), 1)
	go func() {
		release, err := acquireTestSlot(scheduler, ctx, PriorityHigh)
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- release
	}()
	// Let the waiter find both pools full and block.
	time.Sleep(100 * time.Millisecond)
	normal()

	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("high-priority acquire failed instead of taking the released normal slot")
		}
		defer release()
	case <-time.After(time.Second):
		t.Fatal("high-priority acquire stayed blocked on the reserve after a normal slot was released")
	}
	if stats := scheduler.Stats(); stats.NormalInUse != 1 || stats.HighInUse != 1 {
		t.Fatalf("slots = normal %d high %d, want the waiter on normal and the reserve still held", stats.NormalInUse, stats.HighInUse)
	}
}

func TestSchedulerFileBudgetStable(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 8, HighReserve: 0, PrefetchLimit: 1})
	ctx := context.Background()

	var releases []func()
	for i := 0; i < 4; i++ {
		release, err := scheduler.AcquireRange(ctx, "file", 4, PriorityNormal)
		if err != nil {
			t.Fatalf("file acquire %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	defer func() {
		for _, release := range releases {
			release()
		}
	}()

	blockedCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	release, err := scheduler.AcquireRange(blockedCtx, "file", 2, PriorityNormal)
	if err == nil {
		release()
		t.Fatal("a different limit acquired past the per-file cap")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSchedulerPrefetchCapacity(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 4, HighReserve: 0, PrefetchLimit: 2})
	for i := 0; i < 2; i++ {
		if !scheduler.TryAcquirePrefetch() {
			t.Fatalf("prefetch reserve %d failed", i)
		}
	}
	if scheduler.TryAcquirePrefetch() {
		t.Fatal("prefetch reserved past capacity")
	}
	scheduler.ReleasePrefetch()
	scheduler.ReleasePrefetch()
	if stats := scheduler.Stats(); stats.PrefetchInUse != 0 {
		t.Fatalf("prefetch in use = %d, want 0", stats.PrefetchInUse)
	}
}

func TestSchedulerGrantStreams(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 8, HighReserve: 0, PrefetchLimit: 1})
	release, err := scheduler.AcquireRange(context.Background(), "file", 2, PriorityNormal)
	if err != nil {
		t.Fatalf("acquire file budget: %v", err)
	}
	if got := scheduler.GrantStreams(4, "file", 4); got != 2 {
		t.Fatalf("granted streams = %d, want the live budget 2", got)
	}
	release()
	if got := scheduler.GrantStreams(4, "file", 4); got != 4 {
		t.Fatalf("granted streams after release = %d, want profile limit 4", got)
	}
}

func TestSchedulerForegroundAndNonParallel(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 4, HighReserve: 0, PrefetchLimit: 1})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scheduler.BeginForeground()
			scheduler.EndForeground()
		}()
	}
	wg.Wait()
	if got := scheduler.ForegroundCount(); got != 0 {
		t.Fatalf("foreground count = %d, want 0", got)
	}
	if scheduler.IsNonParallel("file") {
		t.Fatal("file marked non-parallel before MarkNonParallel")
	}
	scheduler.MarkNonParallel("file")
	if !scheduler.IsNonParallel("file") {
		t.Fatal("file was not marked non-parallel")
	}
}
