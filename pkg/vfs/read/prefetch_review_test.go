package read

// Regression tests for the consumption-credit review. Each one pins a rule the
// controller must keep regardless of how the caller splits its reads:
// consumption is counted per delivered byte rather than per admitted read, a
// grown budget wakes the worker even when the cursor does not move, the active
// window depth follows the budget instead of the stage maximum, and a lowered
// stage ceiling always drops the old credit.

import (
	"context"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// TestSessionCreditsSplitsInsideOneChunk covers the case where a delivery does
// not admit a prefetch because it stays inside an already confirmed chunk. The
// bytes were still consumed, so the budget must grow exactly as it would for a
// single larger read over the same range.
func TestSessionCreditsSplitsInsideOneChunk(t *testing.T) {
	run := func(name string, sizes []int64) [3]int64 {
		r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
		offset := int64(0)
		for index, size := range sizes {
			r.read(t, sessionContext(1, uint64(index+1)), offset, size)
			offset += size
		}
		p := sessionWorker(t, r, 1)
		horizon, credit, delivered := sessionBudget(p)
		t.Logf("%s: H=%d credit=%d delivered=%d", name, horizon, credit, delivered)
		return [3]int64{horizon, credit, delivered}
	}

	whole := run("one read", []int64{ChunkSize})
	split := run("two halves", []int64{ChunkSize / 2, ChunkSize / 2})
	if whole != split {
		t.Fatalf("the same delivered range earned different budgets: whole=%v split=%v", whole, split)
	}
}

// TestSessionGrowthWakesWithoutCursorMove reads 512 KiB, 512 KiB, 512 KiB-1 and
// a final byte. The last byte completes a full budget of consumption, so the
// horizon doubles while the cursor stays where it was: the worker must wake to
// fill the newly admitted range anyway.
func TestSessionGrowthWakesWithoutCursorMove(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, drive.DefaultReadPolicy())
	// covered reports whether the admitted prefetch ranges cover [start, end).
	covered := func(start, end int64) bool {
		spans := make([][2]int64, 0, 4)
		for _, call := range prefetchCalls(r) {
			spans = append(spans, [2]int64{call.Offset, call.Offset + call.Size})
		}
		for offset := start; offset < end; {
			next := offset
			for _, span := range spans {
				if span[0] <= offset && span[1] > offset {
					next = max(next, span[1])
				}
			}
			if next == offset {
				return false
			}
			offset = next
		}
		return true
	}

	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize-1)
	// Let the one-megabyte budget finish filling chunks 3 and 4, so the grown
	// range is a separate admission rather than part of the earlier window.
	waitUntil(t, "budget fill", func() bool { return covered(3*ChunkSize, 5*ChunkSize) })
	waitPrefetchIdle(t, r)
	p := sessionWorker(t, r, 1)
	horizon, _, _ := sessionBudget(p)
	if horizon != 2*ChunkSize {
		t.Fatalf("horizon before growth = %d, want %d", horizon, 2*ChunkSize)
	}

	r.read(t, sessionContext(1, 4), 3*ChunkSize-1, 1)
	if horizon, credit, _ := sessionBudget(p); horizon != 4*ChunkSize || credit != 0 {
		t.Fatalf("growth without a cursor move: H=%d credit=%d", horizon, credit)
	}
	// The grown horizon reaches two chunks past the old one; the worker must
	// admit them even though every request and the cursor stayed put.
	waitUntil(t, "growth refill", func() bool { return covered(5*ChunkSize, 7*ChunkSize) })
}

// TestSessionDepthFollowsBudget pins D = min(Dcap, ceil(H/Wcap)). The stage
// maximum is a ceiling, not a target: a budget smaller than one full window
// keeps exactly one active window so a short pass does not fan out.
func TestSessionDepthFollowsBudget(t *testing.T) {
	cases := []struct {
		name     string
		horizon  int64
		window   int
		depthCap int
		want     int
	}{
		{"budget below one window", 2 * ChunkSize, 16, 4, 1},
		{"budget exactly one window", 2 * ChunkSize, 2, 4, 1},
		{"budget of two small windows", 4 * ChunkSize, 2, 4, 2},
		{"budget above the depth cap", 8 * ChunkSize, 2, 4, 4},
		{"budget at the depth cap", 64 * ChunkSize, 16, 4, 4},
		{"seed budget", ChunkSize, 16, 4, 1},
		{"stage depth of one", 8 * ChunkSize, 2, 1, 1},
	}
	for _, test := range cases {
		p := &prefetchSession{
			horizon: test.horizon,
			plan:    ReadPlan{PrefetchChunks: test.window, PrefetchDepth: test.depthCap},
		}
		if got := p.depthChunks(); got != test.want {
			t.Errorf("%s: depth = %d, want %d", test.name, got, test.want)
		}
	}
}

// TestSessionLoweredCeilingDropsCreditBelowBudget lowers the stage ceiling from
// 32 MiB to 16 MiB while H is only 2 MiB. The old credit was earned against the
// wider allowance, so it must not survive even though H still fits.
func TestSessionLoweredCeilingDropsCreditBelowBudget(t *testing.T) {
	p := &prefetchSession{wake: make(chan struct{}, 1), completed: map[int64]bool{}}
	wide := ReadPlan{
		Stage:          StageSequential,
		PrefetchChunks: 16,
		PrefetchDepth:  4,
		PrefetchMode:   drive.PrefetchAdaptive,
	}
	deliver(p, wide, 0, 1<<20)
	deliver(p, wide, 1<<20, ChunkSize)
	if horizon, credit, _ := sessionBudget(p); horizon != 2<<20 || credit != ChunkSize {
		t.Fatalf("setup: H=%d credit=%d", horizon, credit)
	}

	narrow := wide
	narrow.PrefetchDepth = 2 // 8 MiB window, 16 MiB ceiling
	reasons, _ := p.update(context.Background(), drive.Entry{ID: "0/file.bin", Size: 1 << 40}, 2<<20/ChunkSize+1, narrow, deliveredRange{})
	if len(reasons) == 0 || reasons[0] != budgetCap {
		t.Fatalf("lowered ceiling reasons = %v, want %q", reasons, budgetCap)
	}
	horizon, credit, delivered := sessionBudget(p)
	if horizon != 2<<20 || credit != 0 {
		t.Fatalf("lowered ceiling kept stale credit: H=%d credit=%d", horizon, credit)
	}
	if delivered != 1<<20+ChunkSize {
		t.Fatalf("delivered = %d, want the pass's consumed end", delivered)
	}

	// Restoring the wider ceiling makes room for later growth, but must not
	// grow on its own.
	p.update(context.Background(), drive.Entry{ID: "0/file.bin", Size: 1 << 40}, 2<<20/ChunkSize+1, wide, deliveredRange{})
	if horizon, credit, _ := sessionBudget(p); horizon != 2<<20 || credit != 0 {
		t.Fatalf("a raised ceiling moved the budget: H=%d credit=%d", horizon, credit)
	}
}

// TestSessionCreditDoesNotStartController covers a probe that repeats inside one
// chunk at a non-zero offset: it is not yet confirmed sequential, so it must not
// admit a prefetch, and its delivery accounting must not start a controller
// either. Otherwise a short probe pays for speculative traffic nobody asked for.
func TestSessionCreditDoesNotStartController(t *testing.T) {
	r := newStagedReaderWithProfile(t, 16*ChunkSize, drive.DefaultReadPolicy())
	offset := int64(3*ChunkSize + ChunkSize/8)
	r.read(t, sessionContext(1, 1), offset, ChunkSize/8)
	r.read(t, sessionContext(1, 2), offset+int64(ChunkSize/8), ChunkSize/8)
	if controllers := sessionCount(r); controllers != 0 {
		t.Fatalf("an unconfirmed probe created %d prefetch controllers", controllers)
	}
	if calls := prefetchCalls(r); len(calls) != 0 {
		t.Fatalf("an unconfirmed probe prefetched %v", calls)
	}
	// The same bytes at offset zero do start a session: the run is admitted
	// there, and the credit path may then keep crediting it.
	r.read(t, sessionContext(1, 3), 0, ChunkSize/8)
	if controllers := sessionCount(r); controllers != 1 {
		t.Fatalf("offset zero created %d prefetch controllers, want 1", controllers)
	}
}

// TestSessionFixedSwitchDropsAdaptiveCredit switches from an adaptive stage to
// a fixed stage whose total budget happens to equal the current budget. The
// adaptive credit was earned under a growth rule that no longer applies, so the
// switch must still reset it and report the mode change instead of returning
// early because the two budgets are equal.
func TestSessionFixedSwitchDropsAdaptiveCredit(t *testing.T) {
	p := &prefetchSession{wake: make(chan struct{}, 1), completed: map[int64]bool{}}
	wide := ReadPlan{
		Stage:          StageSequential,
		PrefetchChunks: 16, // 8 MiB window
		PrefetchDepth:  4,  // 32 MiB ceiling
		PrefetchMode:   drive.PrefetchAdaptive,
	}
	deliver(p, wide, 0, 4<<20)
	if horizon, credit, _ := sessionBudget(p); horizon != 4<<20 || credit != 1<<20 {
		t.Fatalf("setup: H=%d credit=%d, want H=%d credit=%d", horizon, credit, 4<<20, 1<<20)
	}

	fixed := ReadPlan{
		Stage:          StageSequential,
		PrefetchChunks: 8, // 4 MiB window
		PrefetchDepth:  1, // 4 MiB total, exactly the current budget
		PrefetchMode:   drive.PrefetchFixed,
	}
	entry := drive.Entry{ID: "0/file.bin", Size: 1 << 40}
	reasons, _ := p.update(context.Background(), entry, 4<<20/ChunkSize+1, fixed, deliveredRange{})
	if len(reasons) == 0 || reasons[0] != budgetFixed {
		t.Fatalf("switch to fixed reasons = %v, want %q", reasons, budgetFixed)
	}
	horizon, credit, _ := sessionBudget(p)
	if horizon != 4<<20 || credit != 0 {
		t.Fatalf("switch to fixed kept adaptive credit: H=%d credit=%d, want H=%d credit=0", horizon, credit, 4<<20)
	}

	// A repeated fixed update changes nothing and stays quiet.
	if reasons, _ := p.update(context.Background(), entry, 4<<20/ChunkSize+1, fixed, deliveredRange{}); len(reasons) != 0 {
		t.Fatalf("repeated fixed update reported %v, want no transition", reasons)
	}
}
