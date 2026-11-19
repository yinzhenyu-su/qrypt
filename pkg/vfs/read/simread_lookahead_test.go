package read

import (
	"sort"
	"sync"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/testsupport/simread"
)

// TestSimLookaheadDoesNotRefetchInFlightTail is the duplicate-lookahead
// regression test: when a read has already admitted a window covering chunks
// 4..6 and that window is still in flight, the next lookahead schedule must
// advance past the covered tail instead of re-fetching chunks 5..7. Asserting
// on the simulated provider's range log proves the fix end-to-end, at the
// scope where a duplicated tail would cost a real Range request.
func TestSimLookaheadDoesNotRefetchInFlightTail(t *testing.T) {
	const chunkCount = 16
	t.Setenv("QRYPT_FG_SPLIT", "")
	content := make([]byte, chunkCount*ChunkSize)
	for i := range content {
		content[i] = byte(i % 251)
	}
	base := drive.NewFakeDriver()
	if err := base.Seed(map[string]string{"file.bin": string(content)}); err != nil {
		t.Fatalf("seed fake driver: %v", err)
	}
	gate := make(chan struct{})
	var closeGate sync.Once
	release := func() { closeGate.Do(func() { close(gate) }) }
	sim := simread.Wrap(base, simread.Config{
		Gate: func(offset, size int64) <-chan struct{} { return gate },
	})
	source, err := drive.NewReadSource(sim, nil)
	if err != nil {
		t.Fatalf("new read source: %v", err)
	}
	state := NewState(nil)
	entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: int64(len(content))}
	reader := newTestReader(testReaderDeps{
		Host:   &policyHost{driver: sim, entry: entry, cacheKey: "cache"},
		State:  state,
		Source: source,
	})
	t.Cleanup(func() {
		release()
		waitUntil(t, "prefetch completion", func() bool {
			return state.scheduler.Stats().PrefetchInUse == 0 && reader.prefetchInFlight("cache") == 0
		})
		_ = state.Close()
		_ = source.Close()
	})

	ctx := sessionContext(1, 1)
	// A read already admitted a window covering chunks 4..6; it is still in
	// flight when the next read advances the cursor into the same range.
	if load := reader.startPrefetchWindow(ctx, entry, 4, 3, true); load == nil {
		t.Fatal("startPrefetchWindow refused the first window")
	}
	waitUntil(t, "first prefetch range", func() bool { return sim.Stats().Requests >= 1 })
	reader.schedulePrefetch(ctx, entry, 4, 1, true, sequentialLookaheadPlan(2, 4))
	waitUntil(t, "scheduled prefetch ranges", func() bool { return sim.Stats().Requests >= 4 })

	// Assert while the first window is still in flight, before the gate opens:
	// the request log must already be the final one.
	inFlight := sim.Requests()
	if len(inFlight) != 4 {
		t.Fatalf("in-flight lookahead opened %d ranges, want 4: %+v", len(inFlight), inFlight)
	}
	// Chunks 4..12 are covered exactly once: the in-flight window 4..6 plus the
	// remaining horizon windows 7..8, 9..10 and 11..12. A re-fetched tail would
	// show up as an overlap in the tiling.
	assertRangeTiling(t, inFlight, 4*ChunkSize, 13*ChunkSize)
	byOffset := append([]simread.Request(nil), inFlight...)
	sort.Slice(byOffset, func(i, j int) bool { return byOffset[i].Offset < byOffset[j].Offset })
	want := []struct{ offset, size int64 }{
		{4 * ChunkSize, 3 * ChunkSize},
		{7 * ChunkSize, 2 * ChunkSize},
		{9 * ChunkSize, 2 * ChunkSize},
		{11 * ChunkSize, 2 * ChunkSize},
	}
	if len(byOffset) != len(want) {
		t.Fatalf("ranges = %+v, want %d windows", byOffset, len(want))
	}
	for i, w := range want {
		if byOffset[i].Offset != w.offset || byOffset[i].Size != w.size {
			t.Fatalf("range %d = [%d,+%d), want [%d,+%d): %+v",
				i, byOffset[i].Offset, byOffset[i].Size, w.offset, w.size, byOffset)
		}
	}
	if deduped, overlapping := state.LookaheadStats(); deduped == 0 || overlapping != 0 {
		t.Fatalf("lookahead accounting = (deduped %d, overlapping %d); want the covered chunks counted and no overlap",
			deduped, overlapping)
	}

	release()
	waitUntil(t, "prefetch completion", func() bool {
		return state.scheduler.Stats().PrefetchInUse == 0 && reader.prefetchInFlight("cache") == 0
	})
	// Completion must not add a late duplicate fetch for the covered tail.
	completed := sim.Requests()
	if len(completed) != len(inFlight) {
		t.Fatalf("after completion %d ranges were opened, want the original %d: %+v",
			len(completed), len(inFlight), completed)
	}
	assertRangeTiling(t, completed, 4*ChunkSize, 13*ChunkSize)
	if got, want := sim.Stats().BytesServed, int64(9*ChunkSize); got != want {
		t.Fatalf("lookahead delivered %d bytes, want %d (chunks 4..12 exactly once)", got, want)
	}
}
