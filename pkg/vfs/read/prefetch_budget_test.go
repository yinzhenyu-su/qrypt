package read

// Consumption-credit controller: a known-size mount session starts from a small
// forward budget and doubles it only after the caller has consumed a full
// budget of new bytes. These are deterministic sequences over the controller
// itself; the staged-reader tests cover the wiring from real Read calls.

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func adaptiveSequentialPlan() ReadPlan {
	return ReadPlan{
		Stage:          StageSequential,
		PrefetchChunks: SequentialPrefetchChunks,
		PrefetchDepth:  4,
		PrefetchMode:   drive.PrefetchAdaptive,
	}
}

func fixedSequentialPlan() ReadPlan {
	return ReadPlan{
		Stage:          StageSequential,
		PrefetchChunks: SequentialPrefetchChunks,
		PrefetchDepth:  4,
		PrefetchMode:   drive.PrefetchFixed,
	}
}

func newBudgetSession() *prefetchSession {
	return &prefetchSession{wake: make(chan struct{}, 1), completed: map[int64]bool{}}
}

func sessionBudget(p *prefetchSession) (horizon, credit, delivered int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.horizon, p.credit, p.delivered
}

// deliver applies one successful caller delivery to the controller.
func deliver(p *prefetchSession, plan ReadPlan, offset, size int64) {
	entry := drive.Entry{ID: "0/file.bin", Size: 1 << 40}
	p.update(context.Background(), entry, offset/ChunkSize+1, plan, deliveredRange{offset: offset, size: size})
}

func sessionHorizonChunks(p *prefetchSession) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.horizonChunks()
}

func TestSessionBudgetDoublesPerConsumedBudget(t *testing.T) {
	p := newBudgetSession()
	plan := adaptiveSequentialPlan()
	const mib = int64(1 << 20)
	if plan.seedBytes() != mib {
		t.Fatalf("seed = %d, want 1 MiB for the sequential stage", plan.seedBytes())
	}
	if plan.horizonCapBytes() != 32*mib {
		t.Fatalf("Hcap = %d, want 32 MiB", plan.horizonCapBytes())
	}
	// The seed is 1 MiB and is spent by the first budget of consumption; each
	// later step costs exactly the budget it replaces (design 2: 1/2/4/8/16).
	want := []int64{2, 4, 8, 16, 32}
	offset := int64(0)
	for index, grow := range []int64{1, 2, 4, 8, 16} {
		deliver(p, plan, offset, grow*mib)
		offset += grow * mib
		horizon, _, delivered := sessionBudget(p)
		if horizon != want[index]*mib {
			t.Fatalf("after %d MiB consumed: H=%d, want %d MiB", offset/mib, horizon, want[index])
		}
		if delivered != offset {
			t.Fatalf("delivered end = %d, want %d", delivered, offset)
		}
	}
	// Extra consumption at the ceiling must not bank credit for a later,
	// larger stage ceiling.
	deliver(p, plan, offset, 48*mib)
	horizon, credit, delivered := sessionBudget(p)
	if horizon != 32*mib || credit != 0 || delivered != offset+48*mib {
		t.Fatalf("at ceiling: H=%d credit=%d delivered=%d", horizon, credit, delivered)
	}
}

func TestSessionBudgetSpendsAtMostOneStepPerDelivery(t *testing.T) {
	p := newBudgetSession()
	plan := adaptiveSequentialPlan()
	const mib = int64(1 << 20)
	deliver(p, plan, 0, mib) // seed 1 MiB, spent immediately -> H 2 MiB
	if horizon, _, _ := sessionBudget(p); horizon != 2*mib {
		t.Fatalf("H = %d, want 2 MiB", horizon)
	}
	// One delivery holding exactly one budget's worth of credit still grows by
	// a single doubling, never straight to the ceiling.
	deliver(p, plan, mib, 2*mib)
	if horizon, credit, _ := sessionBudget(p); horizon != 4*mib || credit != 0 {
		t.Fatalf("H=%d credit=%d, want 4 MiB and spent credit", horizon, credit)
	}
	// A single large delivery may cross several steps: worth of credit is
	// deducted step by step.
	deliver(p, plan, 3*mib, 28*mib)
	if horizon, credit, _ := sessionBudget(p); horizon != 32*mib || credit != 0 {
		t.Fatalf("H=%d credit=%d, want the 32 MiB ceiling and no leftover credit", horizon, credit)
	}
}

func TestSessionBudgetSeedUsesSmallStageCeiling(t *testing.T) {
	// The real Initial plan is a one-chunk window with depth four: the seed is
	// the window (512 KiB), not the 1 MiB ceiling or the 2 MiB horizon.
	plan := (PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 4}).ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	if !plan.usesConsumptionCredit() {
		t.Fatal("the default Initial stage must use consumption credit")
	}
	p := newBudgetSession()
	deliver(p, plan, 0, ChunkSize)
	if horizon, _, _ := sessionBudget(p); horizon != ChunkSize*2 {
		t.Fatalf("H = %d, want two chunks after spending the one-chunk seed", horizon)
	}
	deliver(p, plan, ChunkSize, ChunkSize)
	deliver(p, plan, 2*ChunkSize, ChunkSize)
	if horizon, _, _ := sessionBudget(p); horizon != 2*2*ChunkSize {
		t.Fatalf("H = %d, want the stage ceiling", horizon)
	}
	if got := sessionHorizonChunks(p); got != 4 {
		t.Fatalf("horizon chunks = %d, want the 4-chunk stage ceiling", got)
	}
}

func TestSessionBudgetStageCeilingUpDoesNotExpand(t *testing.T) {
	p := newBudgetSession()
	initial := (PlanResolver{Profile: drive.DefaultReadPolicy(), AdaptiveDepth: 4}).ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	deliver(p, initial, 0, ChunkSize)
	deliver(p, initial, ChunkSize, ChunkSize)
	before, _, _ := sessionBudget(p)
	// The sequential stage allows 8 MiB windows and a 32 MiB horizon, but that
	// is a ceiling: only further consumption may grow the current budget.
	deliver(p, adaptiveSequentialPlan(), 2*ChunkSize, 0)
	if after, _, _ := sessionBudget(p); after != before {
		t.Fatalf("stage upgrade changed H from %d to %d", before, after)
	}
}

func TestSessionBudgetCapDropClampsAndClearsCredit(t *testing.T) {
	p := newBudgetSession()
	plan := adaptiveSequentialPlan()
	deliver(p, plan, 0, 1<<20)
	deliver(p, plan, 1<<20, 2<<20) // H = 4 MiB
	deliver(p, plan, 3<<20, 1<<20) // credit 1 MiB, below H
	if horizon, _, _ := sessionBudget(p); horizon != 4<<20 {
		t.Fatalf("H = %d, want 4 MiB", horizon)
	}
	// Foreground pressure lowers Dcap to one window: Hcap = Wcap = 8 MiB is
	// still above H, so use a plan whose Wcap is one chunk to force a clamp.
	narrow := ReadPlan{Stage: StageSequential, PrefetchChunks: 2, PrefetchDepth: 1, PrefetchMode: drive.PrefetchAdaptive}
	deliver(p, narrow, 4<<20, 0)
	horizon, credit, _ := sessionBudget(p)
	if horizon != 2*ChunkSize {
		t.Fatalf("H = %d, want the 2-chunk ceiling after the drop", horizon)
	}
	if credit != 0 {
		t.Fatalf("credit = %d, want the old expansion credit cleared", credit)
	}
}

func TestSessionBudgetModeSwitchResets(t *testing.T) {
	p := newBudgetSession()
	adaptive := adaptiveSequentialPlan()
	deliver(p, adaptive, 0, 1<<20)
	deliver(p, adaptive, 1<<20, 2<<20)
	if horizon, _, _ := sessionBudget(p); horizon != 4<<20 {
		t.Fatalf("H = %d, want 4 MiB", horizon)
	}
	// adaptive -> fixed executes the fixed plan literally, with no credit.
	deliver(p, fixedSequentialPlan(), 3<<20, ChunkSize)
	horizon, credit, _ := sessionBudget(p)
	if horizon != 32<<20 || credit != 0 {
		t.Fatalf("fixed H=%d credit=%d, want Hcap with no credit", horizon, credit)
	}
	// fixed -> adaptive starts a fresh pass at the seed.
	deliver(p, adaptive, 3<<20+ChunkSize, ChunkSize)
	horizon, credit, delivered := sessionBudget(p)
	if horizon != 1<<20 || credit != ChunkSize || delivered != 4<<20 {
		t.Fatalf("adaptive reset H=%d credit=%d delivered=%d, want seed plus one chunk", horizon, credit, delivered)
	}
}

func TestSessionCreditCountsOnlyUniqueSuffix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		deliveries []deliveredRange
		credit     int64
		delivered  int64
	}{
		{
			name:       "first delivery counts actual bytes",
			deliveries: []deliveredRange{{offset: 0, size: ChunkSize}},
			credit:     ChunkSize, delivered: ChunkSize,
		},
		{
			name:       "duplicate page adds nothing",
			deliveries: []deliveredRange{{offset: 0, size: ChunkSize}, {offset: 0, size: ChunkSize}, {offset: 0, size: ChunkSize}},
			credit:     ChunkSize, delivered: ChunkSize,
		},
		{
			name:       "partial tail overlap counts only the new suffix",
			deliveries: []deliveredRange{{offset: 0, size: ChunkSize}, {offset: ChunkSize - 4096, size: 8192}},
			credit:     ChunkSize + 4096, delivered: ChunkSize + 4096,
		},
		{
			name:       "forward gap skips the undelivered bytes",
			deliveries: []deliveredRange{{offset: 0, size: ChunkSize}, {offset: 8 * ChunkSize, size: ChunkSize / 4}},
			credit:     ChunkSize + ChunkSize/4, delivered: 8*ChunkSize + ChunkSize/4,
		},
		{
			name:       "empty delivery is not consumption",
			deliveries: []deliveredRange{{offset: 0, size: 0}, {offset: 4 * ChunkSize, size: 0}},
			credit:     0, delivered: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newBudgetSession()
			// A wide ceiling keeps growth out of the way of the suffix rule.
			plan := ReadPlan{Stage: StageSequential, PrefetchChunks: 64, PrefetchDepth: 4, PrefetchMode: drive.PrefetchAdaptive}
			for _, d := range tc.deliveries {
				p.update(context.Background(), drive.Entry{ID: "0/f", Size: 1 << 40}, 1, plan, d)
			}
			_, credit, delivered := sessionBudget(p)
			if credit != tc.credit || delivered != tc.delivered {
				t.Fatalf("credit=%d delivered=%d, want %d/%d", credit, delivered, tc.credit, tc.delivered)
			}
		})
	}
}

func TestSessionCreditIgnoresRequestGranularity(t *testing.T) {
	// The same bytes delivered as one large read or as many small reads must
	// produce the same credit and the same budget: the rule is bytes, not
	// request count.
	plan := adaptiveSequentialPlan()
	large := newBudgetSession()
	deliver(large, plan, 0, 4*ChunkSize)

	small := newBudgetSession()
	for offset := int64(0); offset < 4*ChunkSize; offset += ChunkSize / 4 {
		deliver(small, plan, offset, ChunkSize/4)
	}
	largeHorizon, largeCredit, largeDelivered := sessionBudget(large)
	smallHorizon, smallCredit, smallDelivered := sessionBudget(small)
	if largeHorizon != smallHorizon || largeCredit != smallCredit || largeDelivered != smallDelivered {
		t.Fatalf("granularity changed the budget: large=(%d,%d,%d) small=(%d,%d,%d)",
			largeHorizon, largeCredit, largeDelivered, smallHorizon, smallCredit, smallDelivered)
	}
}

func TestSessionBudgetHorizonIsChunkAlignedAndBounded(t *testing.T) {
	p := newBudgetSession()
	plan := ReadPlan{Stage: StageInitial, PrefetchChunks: 1, PrefetchDepth: 1, PrefetchMode: drive.PrefetchAdaptive}
	deliver(p, plan, 0, ChunkSize)
	if got := sessionHorizonChunks(p); got != 1 {
		t.Fatalf("horizon chunks = %d, want one chunk for a one-chunk ceiling", got)
	}
	// EOF clipping happens in the fill, not in the budget: the horizon may
	// exceed the file end, while no speculative window may start past it.
	r := newStagedReaderWithProfile(t, 6*ChunkSize+17, drive.DefaultReadPolicy())
	for chunk := int64(0); chunk < 6; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
	}
	for _, call := range r.host.recorded() {
		if call.Prefetch && call.Offset >= int64(6*ChunkSize+17) {
			t.Fatalf("prefetch started past EOF: %+v", call)
		}
	}
}

// Test-only accessors keep the controller's fields private to the package but
// readable from assertions without racing the worker.
func (p *prefetchSession) completedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.completed)
}

func (p *prefetchSession) cursorChunk() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursor
}

func (p *prefetchSession) planValue() ReadPlan {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.plan
}
