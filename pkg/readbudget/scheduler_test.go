package readbudget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSchedulerSlotPriority(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 2, HighReserve: 1, PrefetchLimit: 1})
	ctx := context.Background()

	normal, err := scheduler.AcquireSlot(ctx, PriorityNormal)
	if err != nil {
		t.Fatalf("normal acquire: %v", err)
	}
	high, err := scheduler.AcquireSlot(ctx, PriorityHigh)
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

func TestSchedulerFileBudgetStable(t *testing.T) {
	scheduler := NewScheduler(SchedulerConfig{MaxConcurrency: 8, HighReserve: 0, PrefetchLimit: 1})
	ctx := context.Background()

	var releases []func()
	for i := 0; i < 4; i++ {
		release, err := scheduler.AcquireFile(ctx, "file", 4)
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
	release, err := scheduler.AcquireFile(blockedCtx, "file", 2)
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
	release, err := scheduler.AcquireFile(context.Background(), "file", 2)
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
