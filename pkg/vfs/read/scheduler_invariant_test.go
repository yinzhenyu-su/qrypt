package read

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func assertGlobalCapacity(t *testing.T, state *State) {
	t.Helper()
	normal := len(state.slots.normal)
	high := len(state.slots.high)
	if normal > cap(state.slots.normal) || high > cap(state.slots.high) {
		t.Fatalf("slot overflow: normal=%d/%d high=%d/%d", normal, cap(state.slots.normal), high, cap(state.slots.high))
	}
	if normal+high > MaxConcurrency {
		t.Fatalf("global slots in use = %d, want at most %d", normal+high, MaxConcurrency)
	}
}

func TestSchedulerCapacityInvariants(t *testing.T) {
	state := NewState(nil)
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: state})
	ctx := context.Background()

	var releases []func()
	for i := 0; i < cap(state.slots.normal); i++ {
		release, err := reader.acquireReadSlot(ctx)
		if err != nil {
			t.Fatalf("normal acquire %d: %v", i, err)
		}
		releases = append(releases, release)
		assertGlobalCapacity(t, state)
	}
	for i := 0; i < cap(state.slots.high); i++ {
		release, err := reader.acquireReadSlot(WithPriority(ctx, PriorityHigh))
		if err != nil {
			t.Fatalf("high acquire %d: %v", i, err)
		}
		releases = append(releases, release)
		assertGlobalCapacity(t, state)
	}
	if got := len(state.slots.normal) + len(state.slots.high); got != MaxConcurrency {
		t.Fatalf("global slots in use = %d, want %d", got, MaxConcurrency)
	}
	for _, release := range releases {
		release()
	}
	if got := len(state.slots.normal) + len(state.slots.high); got != 0 {
		t.Fatalf("global slots after release = %d, want 0", got)
	}

	var parallelReleases []func()
	for i := 0; i < 4; i++ {
		release, err := state.acquireParallel(ctx, "file", 4)
		if err != nil {
			t.Fatalf("parallel acquire %d: %v", i, err)
		}
		parallelReleases = append(parallelReleases, release)
		state.parallel.mu.Lock()
		got := len(state.parallel.files["file"].ch)
		state.parallel.mu.Unlock()
		if got > 4 {
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
	state.parallel.mu.Lock()
	files := len(state.parallel.files)
	state.parallel.mu.Unlock()
	if files != 0 {
		t.Fatalf("per-file limiter entries after release = %d, want 0", files)
	}

	for i := 0; i < PrefetchLimit; i++ {
		key := fmt.Sprintf("prefetch-%d", i)
		if !state.reservePrefetch(key) {
			t.Fatalf("prefetch reserve %d failed below capacity", i)
		}
		state.prefetch.mu.Lock()
		got := len(state.prefetch.sem)
		state.prefetch.mu.Unlock()
		if got > PrefetchLimit {
			t.Fatalf("prefetch permits in use = %d, want at most %d", got, PrefetchLimit)
		}
	}
	if state.reservePrefetch("prefetch-overflow") {
		t.Fatal("prefetch reserved past capacity")
	}
	for i := 0; i < PrefetchLimit; i++ {
		state.releasePrefetch(fmt.Sprintf("prefetch-%d", i))
	}
	state.prefetch.mu.Lock()
	permits := len(state.prefetch.sem)
	state.prefetch.mu.Unlock()
	if permits != 0 {
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
	if got := len(state.slots.normal) + len(state.slots.high); got != 0 {
		t.Fatalf("global slots after concurrent acquire/release = %d, want 0", got)
	}
}
