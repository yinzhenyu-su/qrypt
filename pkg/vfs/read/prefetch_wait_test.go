package read

// A speculative window with no useful target progress must permit bounded
// foreground takeover. A received block can be reused without waiting for its
// lookahead tail; paced-body tests cover progress slower than the wait budget.

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// quarkLikeProfile keeps one stream per window, so a foreground read and a
// speculative window share the same coalescing shape.
func quarkLikeProfile() drive.ReadPolicy {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 1
	return profile
}

// waitForWideLookahead returns the first speculative Range wider than the
// foreground's single chunk, which is the window these tests hold open. An
// adaptive pass reaches it after consuming its seed, so it is not required to
// be the stage's full sequential window.
func waitForWideLookahead(t *testing.T, r *stagedReader, offset int64) stagedCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range r.host.recorded() {
			if call.Prefetch && call.Size > ChunkSize && call.Offset == offset {
				return call
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no wide lookahead at %d issued: %+v", offset, r.host.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}

// establishSequentialRun reads two contiguous chunks without prefetch, so the
// next read both confirms the run and starts the wide lookahead.
func establishSequentialRun(t *testing.T, r *stagedReader) {
	t.Helper()
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, WithoutReadPrefetch(sessionContext(1, 2)), ChunkSize, ChunkSize)
}

func TestForegroundGivesUpOnStalledPrefetchWindow(t *testing.T) {
	// The production budget is three seconds; a short budget exercises the same
	// bypass path without making the test pay for it.
	restore := shortenPrefetchWaitBudget(t, 20*time.Millisecond)
	defer restore()
	r := newStagedReaderWithProfile(t, 64<<20, quarkLikeProfile())
	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = func(_, size int64) (<-chan struct{}, bool) {
		if size > ChunkSize {
			return release, true
		}
		return nil, false
	}
	establishSequentialRun(t, r)

	// The third read confirms the run and starts the wide lookahead.
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	big := waitForWideLookahead(t, r, 3*ChunkSize)

	// The next sequential chunk is covered by the gated window. It must return
	// once the wait budget expires instead of blocking on the whole window.
	started := time.Now()
	target := big.Offset
	got := r.read(t, sessionContext(1, 4), target, ChunkSize)
	waited := time.Since(started)
	if !bytes.Equal(got, r.host.data[target:target+ChunkSize]) {
		t.Fatal("bypassed read returned wrong bytes")
	}
	if waited > prefetchWaitBudget+2*time.Second {
		t.Fatalf("foreground waited %s, want at most about %s", waited, prefetchWaitBudget)
	}
	var own bool
	for _, call := range r.host.recorded() {
		if !call.Prefetch && call.Offset == target && call.Size == ChunkSize {
			own = true
		}
	}
	if !own {
		t.Fatalf("foreground did not fetch its own window: %+v", r.host.recorded())
	}
	// The abandoned lookahead is canceled so it stops duplicating traffic and
	// holding a slot.
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range r.host.recorded() {
			if call.Offset == big.Offset && call.Size == big.Size && call.Err != nil {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned lookahead kept running: %+v", r.host.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFastPrefetchWindowIsStillJoined(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, quarkLikeProfile())
	release := make(chan struct{})
	r.host.gateFn = func(_, size int64) (<-chan struct{}, bool) {
		if size > ChunkSize {
			return release, true
		}
		return nil, false
	}
	establishSequentialRun(t, r)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	big := waitForWideLookahead(t, r, 3*ChunkSize)

	// Release the window well inside the budget: the sequential read must join
	// it, not fetch the chunk a second time.
	close(release)
	target := big.Offset
	if got := r.read(t, sessionContext(1, 4), target, ChunkSize); !bytes.Equal(got, r.host.data[target:target+ChunkSize]) {
		t.Fatal("joined read returned wrong bytes")
	}
	for _, call := range r.host.recorded() {
		if !call.Prefetch && call.Offset == target && call.Size == ChunkSize {
			t.Fatalf("chunk was fetched again although the lookahead completed: %+v", r.host.recorded())
		}
	}
}

func TestParallelForegroundCanTakeOverSingleStreamPrefetch(t *testing.T) {
	p := drive.DefaultReadPolicy()
	p.Bulk.Streams = 3
	p.Bulk.Split = drive.SplitPolicy{PartBytes: 8 << 20, MinParallelBytes: 4 << 20}
	p.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 8 << 20, Depth: 4}
	r := newStagedReaderWithProfile(t, 64<<20, p)
	observer := &recordingObserver{}
	r.reader.observer = observer
	restore := shortenPrefetchWaitBudget(t, 20*time.Millisecond)
	defer restore()
	blocked := make(chan struct{})
	defer close(blocked)
	r.host.gateFn = func(_, size int64) (<-chan struct{}, bool) {
		return blocked, size > 2*ChunkSize
	}
	establishSequentialRun(t, r)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	r.host.waitCall(t, 7) // Three foreground reads plus four reserved lookaheads.
	target := int64(3 * ChunkSize)
	// Keep the session's prefetch policy active: disabling it now retires
	// its controller before waiting, whereas this test exercises takeover of
	// a still-live speculative block with a differently shaped foreground plan.
	ctx, cancel := context.WithTimeout(sessionContext(1, 4), time.Second)
	defer cancel()
	result := <-r.goRead(ctx, target, ChunkSize)
	if result.err != nil || !bytes.Equal(result.data, r.host.data[target:target+ChunkSize]) {
		t.Fatalf("foreground could not take over differently shaped prefetch: %v", result.err)
	}
	if r.host.maxConcurrent() > p.MaxInFlight {
		t.Fatal("takeover exceeded file budget")
	}
	var began, bypassed bool
	for _, event := range observer.recordedDetails() {
		if event.phase != "wait_window_begin" && event.phase != "wait_window_bypass" {
			continue
		}
		if event.extra["window_streams"] != 1 || event.extra["window_prefetch"] != true || event.extra["window_chunks"] != int64(16) || event.extra["wait_budget_us"] != int64(20000) {
			t.Fatalf("missing prefetch dependency diagnostics: %+v", event)
		}
		began = began || event.phase == "wait_window_begin"
		bypassed = bypassed || event.phase == "wait_window_bypass"
	}
	if !began || !bypassed {
		t.Fatalf("missing wait lifecycle: began=%t bypassed=%t", began, bypassed)
	}
}

// shortenPrefetchWaitBudget makes the bypass timer short enough to test the
// takeover path without waiting the production three seconds.
func shortenPrefetchWaitBudget(t *testing.T, budget time.Duration) func() {
	t.Helper()
	previous := prefetchWaitBudget
	prefetchWaitBudget = budget
	return func() { prefetchWaitBudget = previous }
}

// A foreground read that gives up on a slow lookahead must not be merged into
// the abandoned fetch. Cancellation is asynchronous, so the abandoned load can
// still be in the window table when the caller registers its own window; a join
// would hand the caller the speculative cancellation instead of its data. The
// abandoned body here ignores cancellation, which keeps the race deterministic.
func TestWaitWindowBypassTakeoverSurvivesStuckLookahead(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, quarkLikeProfile())
	restore := shortenPrefetchWaitBudget(t, 20*time.Millisecond)
	defer restore()

	blocked := make(chan struct{})
	var gated atomic.Int32
	r.host.holdCancel = true
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == ChunkSize && size == ChunkSize && gated.Add(1) == 1 {
			return blocked, true
		}
		return nil, false
	}
	// The first read opens the lookahead at chunk 1 and leaves it blocked.
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitCall(t, 2)

	// Reading the lookahead chunk gives up on that window and must deliver the
	// data itself instead of waiting for the abandoned fetch.
	done := r.goRead(sessionContext(1, 2), ChunkSize, ChunkSize)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read after giving up the lookahead: %v", result.err)
		}
		if !bytes.Equal(result.data, r.host.data[ChunkSize:2*ChunkSize]) {
			t.Fatal("takeover read returned wrong bytes")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("foreground read stayed merged into the abandoned lookahead")
	}

	// Let the abandoned body return, then wait for it to release its permits.
	close(blocked)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, prefetches, _ := r.state.RuntimeStats(); prefetches == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned lookahead kept its permits: %+v", r.host.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}

// The FUSE sequential upgrade borrows the bulk stream count. It must keep the
// stage's explicit prefetch depth too, otherwise a stage that names only a depth
// silently falls back to the adaptive multi-window lookahead.
func TestFuseSequentialUpgradeKeepsExplicitPrefetchDepth(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 3
	profile.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: SequentialPrefetchChunks * ChunkSize, Depth: 1}
	r := newStagedReaderWithProfile(t, 64<<20, profile)

	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = func(offset, _ int64) (<-chan struct{}, bool) {
		if offset >= 3*ChunkSize {
			return release, true
		}
		return nil, false
	}
	establishSequentialRun(t, r)
	// The third read confirms the run, upgrades to the bulk stream count, and
	// starts the stage lookahead that the upgrade must not reshape.
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)

	// The session worker starts asynchronously after foreground delivery.
	// Wait for its gated Range before inspecting the registered window.
	window := int64(SequentialPrefetchChunks) * ChunkSize
	call := r.host.waitRangeSize(t, window)
	windows, prefetches, _ := r.state.RuntimeStats()
	if windows != 1 || prefetches != 1 {
		t.Fatalf("lookahead windows = %d (permits %d), want the single suggested window", windows, prefetches)
	}
	if !call.Prefetch || call.Offset != 3*ChunkSize {
		t.Fatalf("lookahead call = %+v, want one speculative %d-chunk window at chunk 3", call, SequentialPrefetchChunks)
	}
}
