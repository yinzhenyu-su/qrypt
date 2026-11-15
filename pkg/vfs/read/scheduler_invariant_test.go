package read

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func assertGlobalCapacity(t *testing.T, state *State) {
	t.Helper()
	stats := state.scheduler.Stats()
	if stats.NormalInUse > stats.NormalCapacity || stats.HighInUse > stats.HighCapacity {
		t.Fatalf("slot overflow: normal=%d/%d high=%d/%d", stats.NormalInUse, stats.NormalCapacity, stats.HighInUse, stats.HighCapacity)
	}
	if stats.NormalInUse+stats.HighInUse > MaxConcurrency {
		t.Fatalf("global slots in use = %d, want at most %d", stats.NormalInUse+stats.HighInUse, MaxConcurrency)
	}
}

func TestSchedulerCapacityInvariants(t *testing.T) {
	state := NewState(nil)
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: state})
	ctx := context.Background()
	initial := state.scheduler.Stats()

	var releases []func()
	for i := 0; i < initial.NormalCapacity; i++ {
		release, err := reader.acquireReadSlot(ctx)
		if err != nil {
			t.Fatalf("normal acquire %d: %v", i, err)
		}
		releases = append(releases, release)
		assertGlobalCapacity(t, state)
	}
	for i := 0; i < initial.HighCapacity; i++ {
		release, err := reader.acquireReadSlot(WithPriority(ctx, PriorityHigh))
		if err != nil {
			t.Fatalf("high acquire %d: %v", i, err)
		}
		releases = append(releases, release)
		assertGlobalCapacity(t, state)
	}
	if got := state.scheduler.Stats().NormalInUse + state.scheduler.Stats().HighInUse; got != MaxConcurrency {
		t.Fatalf("global slots in use = %d, want %d", got, MaxConcurrency)
	}
	for _, release := range releases {
		release()
	}
	if got := state.scheduler.Stats().NormalInUse + state.scheduler.Stats().HighInUse; got != 0 {
		t.Fatalf("global slots after release = %d, want 0", got)
	}

	var parallelReleases []func()
	for i := 0; i < 4; i++ {
		release, err := state.acquireParallel(ctx, "file", 4)
		if err != nil {
			t.Fatalf("parallel acquire %d: %v", i, err)
		}
		parallelReleases = append(parallelReleases, release)
		if got := state.scheduler.PerFileInUse("file"); got > 4 {
			t.Fatalf("per-file permits in use = %d, want at most 4", got)
		}
	}
	blockedCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := state.acquireParallel(blockedCtx, "file", 4)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked per-file acquire error = %v, want context.Canceled", err)
	}
	for _, release := range parallelReleases {
		release()
	}
	if files := state.scheduler.Stats().Files; files != 0 {
		t.Fatalf("per-file limiter entries after release = %d, want 0", files)
	}

	for i := 0; i < initial.PrefetchCapacity; i++ {
		if !state.reservePrefetch() {
			t.Fatalf("prefetch reserve %d failed below capacity", i)
		}
		if got := state.scheduler.Stats().PrefetchInUse; got > initial.PrefetchCapacity {
			t.Fatalf("prefetch permits in use = %d, want at most %d", got, initial.PrefetchCapacity)
		}
	}
	if state.reservePrefetch() {
		t.Fatal("prefetch reserved past capacity")
	}
	for i := 0; i < initial.PrefetchCapacity; i++ {
		state.releasePrefetch()
	}
	if permits := state.scheduler.Stats().PrefetchInUse; permits != 0 {
		t.Fatalf("prefetch permits after release = %d, want 0", permits)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				release, err := reader.acquireReadSlot(ctx)
				if err != nil {
					t.Errorf("concurrent acquire: %v", err)
					return
				}
				release()
			}
		}()
	}
	wg.Wait()
	assertGlobalCapacity(t, state)
	if got := state.scheduler.Stats().NormalInUse + state.scheduler.Stats().HighInUse; got != 0 {
		t.Fatalf("global slots after concurrent acquire/release = %d, want 0", got)
	}
}
