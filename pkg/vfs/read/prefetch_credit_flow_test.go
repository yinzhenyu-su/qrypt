package read

// End-to-end checks for the consumption-credit session adapter: what credits
// the budget, what resets it, how the byte range is derived, and which read
// paths deliberately stay out of it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func sessionCount(r *stagedReader) int {
	r.state.scopes.mu.Lock()
	defer r.state.scopes.mu.Unlock()
	n := 0
	for _, scope := range r.state.scopes.scopes {
		scope.mu.Lock()
		if scope.prefetch != nil {
			n++
		}
		scope.mu.Unlock()
	}
	return n
}

func prefetchCalls(r *stagedReader) []stagedCall {
	var out []stagedCall
	for _, call := range r.host.recorded() {
		if call.Prefetch {
			out = append(out, call)
		}
	}
	return out
}

// growToSequential consumes chunks until the session's budget reaches at least
// want, so a test can talk about a widened pass without hard-coding the ramp
// length it took. It also returns the request id and the next contiguous
// chunk, which the controller checks on every update.
func growToSequential(t *testing.T, r *stagedReader, want int64) (*prefetchSession, uint64, int64) {
	t.Helper()
	for chunk := int64(0); chunk < 128; chunk++ {
		request := uint64(chunk + 1)
		r.read(t, sessionContext(1, request), chunk*ChunkSize, ChunkSize)
		p := sessionWorker(t, r, 1)
		if horizon, _, _ := sessionBudget(p); horizon >= want {
			return p, request, chunk + 1
		}
	}
	t.Fatalf("budget never reached %d bytes", want)
	return nil, 0, 0
}

func TestSessionCreditUsesDeliveredBytesOnly(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	p := sessionWorker(t, r, 1)
	horizon, credit, delivered := sessionBudget(p)
	if delivered != ChunkSize || horizon != 2*ChunkSize || credit != 0 {
		t.Fatalf("first delivery: H=%d credit=%d delivered=%d", horizon, credit, delivered)
	}
	// Recording a predicted access is not consumption: the access sequence
	// state may predict this request's end, but the budget must not move.
	r.state.observeReadAccess("cache", AccessHint{SessionID: 1, RequestID: 1}, 32*ChunkSize, 4*ChunkSize)
	if after, afterCredit, afterDelivered := sessionBudget(p); after != horizon || afterCredit != credit || afterDelivered != delivered {
		t.Fatalf("a predicted request end moved the budget: %d/%d/%d -> %d/%d/%d",
			horizon, credit, delivered, after, afterCredit, afterDelivered)
	}
}

// failAtHost fails the Range that starts at offset, leaving other Ranges alone.
type failAtHost struct {
	*stagedHost
	offset int64
}

func (h failAtHost) DriverRead(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset == h.offset {
		return nil, errors.New("provider rejected the range")
	}
	return h.stagedHost.DriverRead(ctx, entry, offset, size)
}

func TestSessionFailedReadGrantsNoCredit(t *testing.T) {
	r := newStagedReader(t, 8*ChunkSize)
	r.reader = newTestReader(testReaderDeps{Host: failAtHost{stagedHost: r.host, offset: 0}, State: r.state})
	if _, err := r.reader.Read(sessionContext(1, 1), "/file.bin", 0, ChunkSize); err == nil {
		t.Fatal("expected the failing read to surface its error")
	}
	if n := sessionCount(r); n != 0 {
		t.Fatalf("a failed read created %d prefetch controllers", n)
	}
	// The next successful read starts the pass from the seed: the failed
	// attempt contributed no delivered bytes. The failed read may still have a
	// speculative window draining; wait for it before building another reader
	// on the same State, because NewReader rewires State.scheduler and the
	// in-flight prefetch goroutine reads it in releasePrefetch.
	waitPrefetchIdle(t, r)
	r.reader = newTestReader(testReaderDeps{Host: r.host, State: r.state})
	r.read(t, sessionContext(1, 2), 0, ChunkSize)
	p := sessionWorker(t, r, 1)
	horizon, credit, delivered := sessionBudget(p)
	if horizon != 2*ChunkSize || credit != 0 || delivered != ChunkSize {
		t.Fatalf("recovered read: H=%d credit=%d delivered=%d", horizon, credit, delivered)
	}
}

func TestSessionStaleRequestCannotAdvanceBudget(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	for chunk := int64(0); chunk < 4; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	p := sessionWorker(t, r, 1)
	before := func() string {
		h, c, d := sessionBudget(p)
		return fmt.Sprintf("%d/%d/%d", h, c, d)
	}()
	// A late completion of request 2 arrives after request 4 was recorded, and
	// claims a large delivery. Only the current request may move the budget.
	plan := ReadPlan{Stage: StageSequential, PrefetchChunks: SequentialPrefetchChunks, PrefetchDepth: 4, PrefetchMode: drive.PrefetchAdaptive}
	r.reader.schedulePrefetch(sessionContext(1, 2), r.host.entry, 3, 1, true, plan, deliveredRange{offset: 4 * ChunkSize, size: 8 * ChunkSize})
	if after := func() string {
		h, c, d := sessionBudget(p)
		return fmt.Sprintf("%d/%d/%d", h, c, d)
	}(); after != before {
		t.Fatalf("stale request moved the budget: %s -> %s", before, after)
	}
}

func TestSessionCreditIgnoresRequestGranularityThroughReader(t *testing.T) {
	// The same 1 MiB delivered as one read or as two half-chunk reads must
	// produce the same budget: the rule is bytes, not request count.
	newReader := func() (*stagedReader, *prefetchSession) {
		r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
		r.read(t, sessionContext(1, 1), 0, ChunkSize)
		return r, sessionWorker(t, r, 1)
	}
	whole, wholeSession := newReader()
	whole.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)

	halves, halvesSession := newReader()
	halves.read(t, sessionContext(1, 2), ChunkSize, ChunkSize/2)
	halves.read(t, sessionContext(1, 3), ChunkSize+ChunkSize/2, ChunkSize/2)

	wholeBudget := func() string {
		h, c, d := sessionBudget(wholeSession)
		return fmt.Sprintf("%d/%d/%d", h, c, d)
	}()
	halvesBudget := func() string {
		h, c, d := sessionBudget(halvesSession)
		return fmt.Sprintf("%d/%d/%d", h, c, d)
	}()
	if wholeBudget != halvesBudget {
		t.Fatalf("granularity changed the budget: whole %s halves %s", wholeBudget, halvesBudget)
	}
	_ = whole
	_ = halves
}

func TestSessionBudgetIsPerSession(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	// Session 1 consumes four chunks; session 2 only one.
	for chunk := int64(0); chunk < 4; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	r.read(t, sessionContext(2, 1), 0, ChunkSize)
	one := sessionWorker(t, r, 1)
	two := sessionWorker(t, r, 2)
	h1, _, d1 := sessionBudget(one)
	h2, _, d2 := sessionBudget(two)
	if h2 != 2*ChunkSize || d2 != ChunkSize {
		t.Fatalf("session 2 inherited another session's state: H=%d delivered=%d", h2, d2)
	}
	if h1 <= h2 {
		t.Fatalf("session 1 did not grow independently: H1=%d H2=%d", h1, h2)
	}
	_ = d1
}

func TestSessionSeekAfterGrowthRestartsFromSeed(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	p, request, _ := growToSequential(t, r, 4<<20)
	if horizon, _, _ := sessionBudget(p); horizon < 4<<20 {
		t.Fatalf("budget did not grow: %d", horizon)
	}
	// A far jump retires the widened pass; a non-zero seek also keeps the
	// original confirmation gate, so the controller is rebuilt only once the
	// following read confirms the new run.
	r.read(t, sessionContext(1, request+1), 40*ChunkSize, ChunkSize)
	if n := sessionCount(r); n != 0 {
		t.Fatalf("a bare seek created %d controllers", n)
	}
	r.read(t, sessionContext(1, request+2), 41*ChunkSize, ChunkSize)
	fresh := sessionWorker(t, r, 1)
	if fresh == p {
		t.Fatal("seek reused the widened controller")
	}
	horizon, credit, delivered := sessionBudget(fresh)
	if horizon != 1<<20 || credit != ChunkSize || delivered != 42*ChunkSize {
		t.Fatalf("seek did not restart from the seed: H=%d credit=%d delivered=%d", horizon, credit, delivered)
	}
	// The widened pass is gone: no controller carries its budget into the new
	// position, which is what the seed assertion above pins.
}

func TestSessionPauseFillsHorizonWithoutGrowing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
		for chunk := int64(0); chunk < 3; chunk++ {
			r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
		}
		synctest.Wait()
		p := sessionWorker(t, r, 1)
		before, _, _ := sessionBudget(p)
		calls := r.host.callCount()
		time.Sleep(5 * time.Second)
		synctest.Wait()
		after, _, _ := sessionBudget(p)
		if after != before {
			t.Fatalf("a pause grew the budget %d -> %d", before, after)
		}
		if r.host.callCount() != calls {
			t.Fatalf("a pause downloaded past the horizon: %+v", r.host.recorded())
		}
		if p.completedCount() != int(before/ChunkSize) {
			t.Fatalf("horizon not filled: completed=%d horizon chunks=%d", p.completedCount(), before/ChunkSize)
		}
	})
}

func TestSessionIdleRetirementRestartsFromSeed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
		p, request, next := growToSequential(t, r, 4<<20)
		growth, _, _ := sessionBudget(p)
		time.Sleep(prefetchSessionIdle + 5*time.Second) // Far longer than the 30 s retirement.
		synctest.Wait()
		if n := sessionCount(r); n != 0 {
			t.Fatalf("idle controller survived retirement: %d", n)
		}
		// Resuming continues the same run but must not inherit the wide budget.
		r.read(t, sessionContext(1, request+1), next*ChunkSize, ChunkSize)
		fresh := sessionWorker(t, r, 1)
		horizon, credit, delivered := sessionBudget(fresh)
		if horizon >= growth {
			t.Fatalf("resume inherited the old budget: %d (old %d)", horizon, growth)
		}
		if horizon != 1<<20 || credit != ChunkSize || delivered != (next+1)*ChunkSize {
			t.Fatalf("resume did not rebuild the seed: H=%d credit=%d delivered=%d", horizon, credit, delivered)
		}
	})
}

func TestSessionRangeStaysInsideHorizon(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	for chunk := int64(0); chunk < 4; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	r.host.waitCall(t, 2)
	waitPrefetchIdle(t, r)
	p := sessionWorker(t, r, 1)
	horizon, _, _ := sessionBudget(p)
	cursor := p.cursorChunk()
	limit := (cursor + horizon/ChunkSize) * ChunkSize
	for _, call := range prefetchCalls(r) {
		if call.end() > limit {
			t.Fatalf("prefetch past the %d-byte horizon: %+v", horizon, call)
		}
		if call.Size > SequentialPrefetchChunks*ChunkSize {
			t.Fatalf("prefetch wider than the stage window: %+v", call)
		}
	}
}

func TestSessionSkipsCoveredPrefixWithoutPushingTail(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	// Build a two-chunk horizon after one consumed chunk (H = 1 MiB): cursor
	// chunk 1, horizon end chunk 3. Chunk 1 is served by the warm-up window, so
	// the tail must not move past chunk 3.
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitCall(t, 2)
	waitPrefetchIdle(t, r)
	r.state.putHotChunk("cache", 1, r.host.data[ChunkSize:2*ChunkSize])
	// Re-announce the same position: the fill reconsiders the horizon.
	p := sessionWorker(t, r, 1)
	r.reader.schedulePrefetch(sessionContext(1, 1), r.host.entry, 0, 1, true, p.planValue(), deliveredRange{})
	waitPrefetchIdle(t, r)
	for _, call := range prefetchCalls(r) {
		if call.Offset > ChunkSize && call.end() > 3*ChunkSize {
			t.Fatalf("skipped prefix pushed the tail past the horizon: %+v", call)
		}
	}
}

func TestSessionOffsetZeroWarmupUsesSeed(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	p := sessionWorker(t, r, 1)
	if p.depthCap() != 4 {
		t.Fatalf("depth cap = %d, want the stage ceiling of four", p.depthCap())
	}
	// Lookahead is speculative: the read returns before either window is
	// admitted, and growth only wakes the controller. Wait for both windows
	// instead of sampling the scheduler, and accept them in either order.
	waitUntil(t, "session windows", func() bool { return len(prefetchCalls(r)) >= 2 })
	waitPrefetchIdle(t, r)
	calls := prefetchCalls(r)
	if len(calls) != 2 {
		t.Fatalf("prefetch windows = %d, want the warm-up plus one horizon window: %+v", len(calls), r.host.recorded())
	}
	// Default Initial stage: Wcap is one chunk, so the seed is one chunk and
	// the warm-up counts as the session's first active window. The budget then
	// pays for exactly one horizon window, which starts where the seed ends.
	offsets := []int64{calls[0].Offset, calls[1].Offset}
	if offsets[0] > offsets[1] {
		offsets[0], offsets[1] = offsets[1], offsets[0]
	}
	if offsets[0] != ChunkSize || offsets[1] != 2*ChunkSize {
		t.Fatalf("prefetch windows = %+v, want the seed at %d and the horizon at %d", calls, ChunkSize, 2*ChunkSize)
	}
	for _, call := range calls {
		if call.Size > ChunkSize {
			t.Fatalf("prefetch window wider than the seed/stage window: %+v", call)
		}
	}
}

func TestSessionNonZeroSeekNeedsConfirmedSequential(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	// A first read at a non-zero offset is a jump, not a stream: no speculative
	// window may be opened before contiguous access is confirmed.
	r.read(t, sessionContext(1, 1), 32*ChunkSize, ChunkSize)
	if calls := prefetchCalls(r); len(calls) != 0 {
		t.Fatalf("random access opened lookahead: %+v", calls)
	}
	if n := sessionCount(r); n != 0 {
		t.Fatalf("random access created a controller: %d", n)
	}
}

func TestSessionSmallBudgetNeverWaitsForABatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
		// The seed horizon is one chunk wide; its batch threshold is one chunk,
		// so the only horizon slot is admitted instead of waiting for a batch.
		r.read(t, sessionContext(1, 1), 0, ChunkSize)
		synctest.Wait()
		if got := len(prefetchCalls(r)); got != 2 {
			t.Fatalf("prefetch windows = %d, want the warm-up and the one horizon slot: %+v", got, r.host.recorded())
		}
		p := sessionWorker(t, r, 1)
		if horizon, _, _ := sessionBudget(p); horizon != 2*ChunkSize {
			t.Fatalf("horizon = %d", horizon)
		}
	})
}

func TestSessionCapShrinkKeepsUsefulLoad(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var gated int
	r.host.gateFn = func(_, size int64) (<-chan struct{}, bool) {
		if size <= ChunkSize {
			return nil, false
		}
		gated++
		return release, true
	}
	// A fixed plan admits its whole lookahead at once, so one run of the
	// controller leaves a wide, still-useful load in flight. Depth stays at
	// one window: deeper lookahead windows queue behind the gated one on the
	// read scheduler and only start after the gate is released, i.e. after the
	// shrink, which would make the "admitted after shrink" count racy.
	wide := ReadPlan{Stage: StageSequential, PrefetchChunks: 8, PrefetchDepth: 1, PrefetchMode: drive.PrefetchFixed}
	advanceSession(r, 1, 1, 1, wide)
	r.host.waitCall(t, 1)
	waitUntil(t, "wide lookahead admission", func() bool { return gated > 0 })
	before := len(prefetchCalls(r))

	// A narrower stage ceiling lowers Hcap. New admissions must respect it,
	// while the already useful in-flight load is allowed to finish.
	narrow := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1, PrefetchMode: drive.PrefetchAdaptive}
	r.state.observeReadAccess("cache", AccessHint{SessionID: 1, RequestID: 2}, ChunkSize, ChunkSize)
	r.reader.schedulePrefetch(sessionContext(1, 2), r.host.entry, 1, 1, true, narrow, deliveredRange{})
	p := sessionWorker(t, r, 1)
	if horizon, credit, _ := sessionBudget(p); horizon > 2*ChunkSize || credit != 0 {
		t.Fatalf("shrink left H=%d credit=%d, want the 2-chunk ceiling and no credit", horizon, credit)
	}
	unblock()
	waitPrefetchIdle(t, r)
	for _, call := range prefetchCalls(r) {
		if call.Err != nil {
			t.Fatalf("a useful in-flight load was canceled by the shrink: %+v", call)
		}
	}
	if after := prefetchCalls(r); len(after) > before {
		t.Fatalf("the lowered ceiling admitted new ranges: %+v", after[before:])
	}
}

func TestSessionBudgetDiagnosticsReconcile(t *testing.T) {
	recorder := &eventRecorder{}
	// The plan snapshot walks the horizon and is therefore gated on debug
	// logging; the credit transitions are not. Enable it so both appear.
	logging.L.SetLevel("debug")
	t.Cleanup(func() { logging.L.SetLevel("info") })
	r := newStagedReaderWithProfile(t, 8<<20, drive.DefaultReadPolicy())
	r.reader.observer = recorder
	for chunk := int64(0); chunk < 4; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	r.host.waitCall(t, 2)
	waitPrefetchIdle(t, r)
	// The plan snapshot is taken when a session fill pass runs, which can
	// trail the recorded Range calls under CPU contention: PrefetchInUse is
	// still zero before the session worker has filled anything. Wait for the
	// event instead of racing the scheduler bookkeeping.
	waitUntil(t, "prefetch plan event", func() bool {
		for _, event := range recorder.snapshot() {
			if event.Phase == "prefetch_plan" {
				return true
			}
		}
		return false
	})

	var changes []string
	var sawPlan bool
	for _, event := range recorder.snapshot() {
		extra := event.Extra
		if extra == nil {
			continue
		}
		for _, forbidden := range []string{"http://", "https://", "cookie", "token=", "auth_key"} {
			if strings.Contains(strings.ToLower(fmt.Sprint(extra)), forbidden) {
				t.Fatalf("%s event leaked %q: %v", event.Phase, forbidden, extra)
			}
		}
		switch event.Phase {
		case "prefetch_credit":
			change, _ := extra["budget_change"].(string)
			if change == "" {
				t.Fatalf("credit event without a change reason: %v", extra)
			}
			for _, key := range []string{"prefetch_mode", "budget_bytes", "credit_bytes", "delivered_end", "window_cap_bytes", "depth_cap", "stage_ceiling_bytes"} {
				if _, ok := extra[key]; !ok {
					t.Fatalf("credit event missing %q: %v", key, extra)
				}
			}
			changes = append(changes, change)
		case "prefetch_plan":
			sawPlan = true
			for _, key := range []string{"prefetch_mode", "budget_bytes", "credit_bytes", "delivered_end", "window_cap_bytes", "depth_cap"} {
				if _, ok := extra[key]; !ok {
					t.Fatalf("plan event missing %q: %v", key, extra)
				}
			}
		}
	}
	if !sawPlan {
		t.Fatal("no prefetch plan event was recorded")
	}
	if len(changes) == 0 || !strings.HasPrefix(changes[0], budgetSeed) {
		t.Fatalf("budget changes do not start with the seed: %v", changes)
	}
	grown := false
	for _, change := range changes {
		grown = grown || strings.Contains(change, budgetGrow)
	}
	if !grown {
		t.Fatalf("no growth was recorded: %v", changes)
	}
}

func TestSessionBudgetDoesNotReadDiagnostics(t *testing.T) {
	// A recorder that panics on any read would prove the controller never
	// consults the projection; instead assert the projection is absent from the
	// controller's inputs by growing a budget with no debug operation at all.
	r := newStagedReaderWithProfile(t, 8<<20, drive.DefaultReadPolicy())
	// The default reader has a no-op observer, so every detail event is dropped
	// on the floor; the budget must still grow.
	for chunk := int64(0); chunk < 4; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	p := sessionWorker(t, r, 1)
	if horizon, _, _ := sessionBudget(p); horizon < 2*ChunkSize {
		t.Fatalf("budget did not grow without diagnostics: %d", horizon)
	}
}

func TestFixedSessionExecutesPlannedShape(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 2 * ChunkSize, Depth: 2}
	r := newStagedReaderWithProfile(t, 64<<20, profile)
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.host.waitCall(t, 4)
	p := sessionWorker(t, r, 1)
	if horizon, credit, _ := sessionBudget(p); horizon != 4*ChunkSize || credit != 0 {
		t.Fatalf("fixed session H=%d credit=%d, want the literal 4-chunk plan", horizon, credit)
	}
	for _, call := range prefetchCalls(r) {
		if call.Size != 2*ChunkSize {
			t.Fatalf("fixed plan issued a %d-byte window: %+v", call.Size, call)
		}
	}
	// Even after far more consumption the fixed shape must not widen.
	for chunk := int64(2); chunk < 12; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	for _, call := range prefetchCalls(r) {
		if call.Size > 2*ChunkSize {
			t.Fatalf("fixed plan widened after consumption: %+v", call)
		}
	}
}

func TestDisabledSessionNeverSchedules(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Interactive.Initial.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}
	profile.Interactive.Seek.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}
	profile.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}
	r := newStagedReaderWithProfile(t, 8<<20, profile)
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	if calls := prefetchCalls(r); len(calls) != 0 {
		t.Fatalf("disabled prefetch issued %+v", calls)
	}
	if n := sessionCount(r); n != 0 {
		t.Fatalf("disabled prefetch created %d controllers", n)
	}
}

func TestProbeBulkAndStreamStayOutsideTheAdapter(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(t *testing.T, r *stagedReader) []stagedCall
	}{
		{
			name: "probe",
			read: func(t *testing.T, r *stagedReader) []stagedCall {
				t.Helper()
				r.read(t, WithReadIntent(sessionContext(1, 1), ReadIntentProbe), 0, ChunkSize)
				return prefetchCalls(r)
			},
		},
		{
			name: "bulk",
			read: func(t *testing.T, r *stagedReader) []stagedCall {
				t.Helper()
				r.read(t, WithReadIntent(sessionContext(1, 1), ReadIntentBulk), 0, ChunkSize)
				return prefetchCalls(r)
			},
		},
		{
			name: "no session",
			read: func(t *testing.T, r *stagedReader) []stagedCall {
				t.Helper()
				r.read(t, context.Background(), 0, ChunkSize)
				return prefetchCalls(r)
			},
		},
		{
			name: "unknown length",
			read: func(t *testing.T, r *stagedReader) []stagedCall {
				t.Helper()
				r.host.entry.Size = 0
				r.read(t, sessionContext(1, 1), 0, ChunkSize)
				return prefetchCalls(r)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStagedReaderWithProfile(t, 8<<20, drive.DefaultReadPolicy())
			calls := tc.read(t, r)
			if tc.name != "bulk" {
				// Bulk keeps its historical path, which for a hinted caller is the
				// session controller executing the bulk window literally.
				if n := sessionCount(r); n != 0 {
					t.Fatalf("%s created %d session controllers", tc.name, n)
				}
			}
			if tc.name == "probe" && len(calls) != 0 {
				t.Fatalf("probe prefetched: %+v", calls)
			}
			// Everything else keeps the bounded legacy burst: depth windows of
			// the planned window size, not a single seed window.
			if tc.name != "probe" && len(calls) > 1 {
				for _, call := range calls[1:] {
					if call.Size > ChunkSize {
						t.Fatalf("%s widened its burst: %+v", tc.name, call)
					}
				}
			}
		})
	}
}

func TestReadStreamKeepsItsOwnScope(t *testing.T) {
	r := newStagedReaderWithProfile(t, 4<<20, drive.DefaultReadPolicy())
	rc, err := r.reader.ReadStream(sessionContext(1, 1), "/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, r.host.data) {
		t.Fatal("stream returned wrong bytes")
	}
	if n := sessionCount(r); n != 0 {
		t.Fatalf("ReadStream created %d session controllers", n)
	}
}

func TestInteractiveSessionAdoptsTheAdapterSingleStream(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	r.read(t, WithReadIntent(sessionContext(1, 1), ReadIntentInteractive), 0, ChunkSize)
	if n := sessionCount(r); n != 1 {
		t.Fatalf("interactive read created %d controllers", n)
	}
	p := sessionWorker(t, r, 1)
	if plan := p.planValue(); plan.MaxStreams != 1 {
		t.Fatalf("interactive plan streams = %d, want one", plan.MaxStreams)
	}
	r.read(t, WithReadIntent(sessionContext(1, 2), ReadIntentInteractive), ChunkSize, ChunkSize)
	for _, call := range prefetchCalls(r) {
		if call.Plan.MaxStreams != 1 {
			t.Fatalf("interactive window planned %d streams: %+v", call.Plan.MaxStreams, call)
		}
		if call.Size > int64(SequentialPrefetchChunks)*ChunkSize {
			t.Fatalf("interactive window exceeded the stage cap: %+v", call)
		}
	}
}
