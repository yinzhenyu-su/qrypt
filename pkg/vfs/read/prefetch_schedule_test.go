package read

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// slidingProfile is the lookahead shape used by these tests: adaptive windows
// are 2 chunks wide and three windows deep after sequential access is observed.
func slidingProfile(budget int) drive.ReadPolicy {
	p := drive.DefaultReadPolicy()
	p.MaxInFlight = budget
	p.Interactive.PrefetchWindowBytes = 2 * ChunkSize
	p.Interactive.PrefetchDepth = 3
	return p
}

func sequentialLookaheadPlan(window, depth int) ReadPlan {
	return ReadPlan{Stage: StageSequential, PrefetchChunks: window, PrefetchDepth: depth}
}

// prefetchWindows maps the starting chunk of each speculative window to its
// chunk count. Prefetch goroutines start concurrently, so call order is not
// meaningful; the set of admitted windows is.
func prefetchWindows(t *testing.T, r *stagedReader) map[int64]int64 {
	t.Helper()
	windows := map[int64]int64{}
	for _, call := range r.host.recorded() {
		if call.Prefetch {
			windows[call.Offset/ChunkSize] = call.Size / ChunkSize
		}
	}
	return windows
}

func waitPrefetchIdle(t *testing.T, r *stagedReader) {
	t.Helper()
	waitUntil(t, "prefetch completion", func() bool {
		return r.state.scheduler.Stats().PrefetchInUse == 0 && r.reader.prefetchInFlight("cache") == 0
	})
}

func TestPrefetchSchedulesFixedHorizon(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	gate := make(chan struct{})
	r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return gate, true }
	plan := sequentialLookaheadPlan(2, 3)
	ctx := sessionContext(1, 1)

	r.reader.schedulePrefetch(ctx, r.host.entry, 2, 1, true, plan)
	r.host.waitCall(t, 3)
	windows := prefetchWindows(t, r)
	for _, want := range []int64{3, 5, 7} {
		if windows[want] != 2 {
			t.Fatalf("windows = %v, want 2-chunk windows at chunks 3, 5 and 7", windows)
		}
	}
	// The horizon is full: repeating the same read must not widen it.
	r.reader.schedulePrefetch(ctx, r.host.entry, 2, 1, true, plan)
	if got := r.host.callCount(); got != 3 {
		t.Fatalf("depth three admitted %d windows", got)
	}
	close(gate)
	waitPrefetchIdle(t, r)
}

func TestPrefetchSkipsWindowsAlreadyInFlight(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	gate := make(chan struct{})
	r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return gate, true }
	ctx := sessionContext(1, 1)
	// A read already admitted a window covering chunks 4..6; it is still in
	// flight when the next read advances the cursor into the same range.
	if load := r.reader.startPrefetchWindow(ctx, r.host.entry, 4, 3, true); load == nil {
		t.Fatal("startPrefetchWindow refused the first window")
	}
	r.host.waitCall(t, 1)
	r.reader.schedulePrefetch(ctx, r.host.entry, 4, 1, true, sequentialLookaheadPlan(2, 4))
	r.host.waitCall(t, 4)
	windows := prefetchWindows(t, r)
	if got := windows[4]; got != 3 {
		t.Fatalf("in-flight window = %v chunks, want 3", got)
	}
	if _, duplicated := windows[5]; duplicated {
		t.Fatalf("scheduler re-fetched the in-flight range: windows = %v", windows)
	}
	for _, want := range []int64{7, 9, 11} {
		if windows[want] != 2 {
			t.Fatalf("windows = %v, want the remaining horizon at 7, 9 and 11", windows)
		}
	}
	if deduped, overlapping := r.state.LookaheadStats(); deduped == 0 || overlapping != 0 {
		t.Fatalf("lookahead accounting = (deduped %d, overlapping %d); want the covered chunks counted and no overlap", deduped, overlapping)
	}
	close(gate)
	waitPrefetchIdle(t, r)
}

func TestPrefetchSkipsResidentChunks(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	r.state.putHotChunk("cache", 3, r.host.data[3*ChunkSize:4*ChunkSize])
	gate := make(chan struct{})
	r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return gate, true }

	r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 2, 1, true, sequentialLookaheadPlan(2, 3))
	r.host.waitCall(t, 3)
	windows := prefetchWindows(t, r)
	if windows[4] != 2 || windows[6] != 2 || windows[8] != 1 {
		t.Fatalf("windows = %v, want the resident chunk skipped and the tail clipped", windows)
	}
	close(gate)
	waitPrefetchIdle(t, r)
}

func TestPrefetchRespectsPrefetchCapacity(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	var releases []func()
	for r.state.scheduler.TryAcquirePrefetch() {
		releases = append(releases, r.state.scheduler.ReleasePrefetch)
	}
	r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 2, 1, true, sequentialLookaheadPlan(2, 3))
	if r.host.callCount() != 0 {
		t.Fatal("prefetch bypassed occupied capacity")
	}
	for _, release := range releases {
		release()
	}
	r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 2, 1, true, sequentialLookaheadPlan(2, 3))
	r.host.waitCall(t, 3)
	waitPrefetchIdle(t, r)
}

func TestPrefetchCanceledScopeStopsInFlight(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	gate := make(chan struct{})
	r.host.gateFn = func(_, _ int64) (<-chan struct{}, bool) { return gate, true }
	r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 2, 1, true, sequentialLookaheadPlan(2, 3))
	r.host.waitCall(t, 3)
	if got := r.reader.prefetchInFlight("cache"); got != 3 {
		t.Fatalf("in-flight windows = %d", got)
	}
	r.state.ReleaseReadSession(1)
	close(gate)
	waitPrefetchIdle(t, r)
	if got := r.host.callCount(); got != 3 {
		t.Fatalf("a released session kept refilling: %d calls", got)
	}
}

type failingPrefetchHost struct{ *stagedHost }

func (h failingPrefetchHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, err := h.stagedHost.DriverRead(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	_ = rc.Close()
	return nil, errors.New("permanent test failure")
}

func TestPrefetchFailureWaitsForTheNextRead(t *testing.T) {
	r := newStagedReader(t, 16*ChunkSize)
	r.reader = newTestReader(testReaderDeps{Host: failingPrefetchHost{r.host}, State: r.state})
	plan := sequentialLookaheadPlan(2, 1)
	ctx := sessionContext(1, 1)

	r.reader.schedulePrefetch(ctx, r.host.entry, 2, 1, true, plan)
	r.host.waitCall(t, 1)
	waitPrefetchIdle(t, r)
	if got := r.host.callCount(); got != 1 {
		t.Fatalf("failure retried without a read: %d calls", got)
	}
	r.reader.schedulePrefetch(ctx, r.host.entry, 2, 1, true, plan)
	r.host.waitCall(t, 2)
	waitPrefetchIdle(t, r)
}

// The horizon is refreshed on every foreground read, so the scheduler must
// report a decision when it admits speculative work or when the stop reason
// changes, not once per 16 KiB read. Without that dedupe a debug mount emits
// thousands of prefetch_plan events for a single playback read.
func TestPrefetchPlanLogsRepeatedDecisionsOnce(t *testing.T) {
	recorder := &eventRecorder{}
	reader := newTestReader(testReaderDeps{Host: stubHost{}, State: NewState(nil)})
	reader.observer = recorder
	entry := drive.Entry{ID: "0/file.bin", Size: 8 << 20}
	ctx := drive.WithDebugOperation(context.Background(), drive.DebugOperation{OpID: "op", Name: "/file.bin"})

	for i := 0; i < 5; i++ {
		reader.logPrefetchPlan(ctx, entry, "cache", 1, 9, "depth", 0)
	}
	if got := countPrefetchPlans(recorder); got != 1 {
		t.Fatalf("prefetch_plan events = %d after five identical depth decisions, want 1", got)
	}

	reader.logPrefetchPlan(ctx, entry, "cache", 1, 9, "covered", 1)      // admitted work
	reader.logPrefetchPlan(ctx, entry, "cache", 1, 9, "not_admitted", 0) // reason changed
	reader.logPrefetchPlan(ctx, entry, "cache", 1, 9, "not_admitted", 0) // repeated again
	if got := countPrefetchPlans(recorder); got != 3 {
		t.Fatalf("prefetch_plan events = %d, want 3 (admission + change + none)", got)
	}
}

func countPrefetchPlans(recorder *eventRecorder) int {
	plans := 0
	for _, event := range recorder.snapshot() {
		if event.Phase == "prefetch_plan" {
			plans++
		}
	}
	return plans
}
