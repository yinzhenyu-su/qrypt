package read

// Stage-policy tests: a driver's window and lookahead numbers must shape the
// foreground window of a read session without changing its intent, its stream
// gate, or its cache admission. Initial and seek access always keep the caller
// window; only the confirmed sequential stage widens it.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func stageProfile() drive.ReadPolicy {
	profile := drive.DefaultReadPolicy()
	profile.Interactive.WindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchWindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchDepth = 2
	return profile
}

func TestStageSeekKeepsCallerWindow(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	// The first read opens the stream; the second jumps somewhere new, which
	// is the seek a player performs.
	r.read(t, sessionContext(1, 1), 0, 64<<10)
	// Far from the initial lookahead: only a real seek can reach it.
	offset := int64(12*ChunkSize) + 1000
	got := r.read(t, sessionContext(1, 2), offset, 64<<10)
	if !bytes.Equal(got, r.host.data[offset:offset+64<<10]) {
		t.Fatal("seek returned wrong bytes")
	}
	target := r.host.rangesOverlapping(offset, offset+64<<10)
	if len(target) == 0 {
		t.Fatal("seek target was never fetched from the provider")
	}
	for _, call := range target {
		if call.Plan.Stage != StageSeek {
			t.Fatalf("seek range plan = %+v, want the seek stage", call.Plan)
		}
		// A request that crosses a chunk boundary covers the two chunks it
		// touches; the seek never widens to a policy window.
		if call.Size > 2*ChunkSize {
			t.Fatalf("seek range size = %d, want at most the touched chunks", call.Size)
		}
	}
}

func TestStageSeekAppliesToInteractive(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	r.read(t, WithReadIntent(sessionContext(1, 1), ReadIntentInteractive), 0, 64<<10)
	ctx := WithReadIntent(sessionContext(1, 2), ReadIntentInteractive)
	got := r.read(t, ctx, 4<<20, 64<<10)
	if !bytes.Equal(got, r.host.data[4<<20:4<<20+64<<10]) {
		t.Fatal("interactive seek returned wrong bytes")
	}
	target := r.host.rangesOverlapping(4<<20, 4<<20+64<<10)
	if len(target) == 0 {
		t.Fatal("interactive seek target was never fetched from the provider")
	}
	for _, call := range target {
		if call.Size > ChunkSize {
			t.Fatalf("interactive seek range size = %d, want at most one chunk", call.Size)
		}
		if call.Plan.MaxStreams != 1 {
			t.Fatalf("interactive streams = %d, want the single-window contract", call.Plan.MaxStreams)
		}
	}
}

func TestStageBulkIgnoresStageSuggestion(t *testing.T) {
	profile := stageProfile()
	profile.Bulk.Streams = 2
	profile.Bulk.MinParallelBytes = 0
	r := newStagedReaderWithProfile(t, 8<<20, profile)
	ctx := WithReadIntent(sessionContext(1, 2), ReadIntentBulk)
	r.read(t, ctx, 4<<20, 64<<10)
	plan := r.host.lastPlan()
	if plan.WindowChunks != byteChunks(profile.Bulk.WindowBytes) {
		t.Fatalf("bulk window = %d, want %d", plan.WindowChunks, byteChunks(profile.Bulk.WindowBytes))
	}
}

func TestStageSequentialWidensWindowAndLookahead(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, stageProfile())
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	// The confirmed sequential stage reads 2-chunk windows and prefetches the
	// declared 2-chunk lookahead instead of the legacy 16-chunk one.
	r.host.waitRangeSize(t, 2*ChunkSize)
	for _, call := range r.host.recorded() {
		if call.Size == int64(2*PrefetchChunks)*ChunkSize {
			t.Fatalf("legacy %d-chunk lookahead used despite the profile", 2*PrefetchChunks)
		}
	}
}

func TestStageZeroDepthDisablesLookahead(t *testing.T) {
	profile := stageProfile()
	profile.Interactive.PrefetchDepth = 0
	r := newStagedReaderWithProfile(t, 64<<20, profile)
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	waitState(t, r.state, "reads to settle", func() bool {
		windows, prefetches, _ := r.state.RuntimeStats()
		return windows == 0 && prefetches == 0
	})
	for _, call := range r.host.recorded() {
		if call.Prefetch {
			t.Fatalf("lookahead ran despite depth zero: %+v", call)
		}
	}
}

func TestStageDepthBoundsLookahead(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 1
	profile.Interactive.PrefetchWindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchDepth = 3
	r := newStagedReaderWithProfile(t, 64<<20, profile)

	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = blockEveryRange(release)

	plan := PlanResolver{Profile: profile}.ResolveStage(StageInitial, ReadIntentUnspecified, 1)
	r.reader.schedulePrefetch(context.Background(), r.host.entry, 0, 1, true, plan)

	// Window registration happens before the fetch starts, so the count is
	// exact even while every window is blocked on the provider.
	windows, prefetches, _ := r.state.RuntimeStats()
	if prefetches != 3 || windows != 3 {
		t.Fatalf("lookahead windows = %d (permits %d), want 3", windows, prefetches)
	}
	const window = int64(2) * ChunkSize
	deadline := time.Now().Add(2 * time.Second)
	for {
		var calls []stagedCall
		for _, call := range r.host.recorded() {
			if call.Prefetch {
				calls = append(calls, call)
			}
		}
		if len(calls) >= 3 {
			got := make(map[int64]int64, len(calls))
			for _, call := range calls {
				got[call.Offset] = call.Size
			}
			for i := 0; i < 3; i++ {
				wantOffset := ChunkSize + int64(i)*window
				if size, ok := got[wantOffset]; !ok || size != window {
					t.Fatalf("lookahead windows = %v, want offset %d size %d", got, wantOffset, window)
				}
			}
			if len(got) != 3 {
				t.Fatalf("lookahead windows = %v, want 3 distinct windows", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lookahead calls = %d, want 3: %+v", len(calls), calls)
		}
		select {
		case <-r.host.changed:
		case <-time.After(time.Millisecond):
		}
	}
}

func TestStagePrefetchDisabledBeatsSuggestion(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	ctx := WithoutReadPrefetch(sessionContext(1, 1))
	r.read(t, ctx, 0, ChunkSize)
	waitState(t, r.state, "read to settle", func() bool {
		windows, prefetches, _ := r.state.RuntimeStats()
		return windows == 0 && prefetches == 0
	})
	if calls := r.host.recorded(); len(calls) != 1 {
		t.Fatalf("ranges = %+v, want only the target read", calls)
	}
}

func TestStageSuggestionKeepsCacheAdmission(t *testing.T) {
	cache := &recordingCache{}
	host := &stagedHost{
		entry:    drive.Entry{ID: "0/file.bin", Size: 8 << 20, ModTime: time.Unix(1700000000, 0)},
		data:     stagedContent(8 << 20),
		cacheKey: "cache",
		changed:  make(chan struct{}, 1),
	}
	state := NewState(cache)
	t.Cleanup(func() { _ = state.Close() })
	reader := newTestReader(testReaderDeps{Host: host, State: state, Profile: stageProfile()})
	// A seek-shaped read under the unspecified intent must still admit its
	// chunks to the durable cache: the stage only changes windows.
	rc, err := reader.Read(sessionContext(1, 2), "/file.bin", int64(ChunkSize)+1000, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if cache.putCount() == 0 {
		t.Fatal("stage suggestion suppressed the durable cache write")
	}
}

func TestStageUnhintedReadStaysInitial(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	// Direct callers carry no session, so no stage may be inferred before the
	// read; the initial policy applies and the window stays the caller's.
	r.read(t, context.Background(), 0, ChunkSize)
	if plan := r.host.lastPlan(); plan.Stage != StageInitial {
		t.Fatalf("unhinted stage = %v, want initial", plan.Stage)
	}
}

// A confirmed sequential stage with depth two keeps two lookahead windows in
// flight, so a player that drains one window still has the next one buffered or
// on the way instead of waiting a full window fetch every time.
func TestStageSequentialDepthTwoKeepsTwoLookaheads(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 1
	profile.Interactive.WindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchWindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchDepth = 2
	r := newStagedReaderWithProfile(t, 64<<20, profile)
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, WithoutReadPrefetch(sessionContext(1, 2)), ChunkSize, ChunkSize)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)

	// Wait until both lookahead windows exist, then assert they are distinct
	// and each is the declared two chunks.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var lookaheads []stagedCall
		for _, call := range r.host.recorded() {
			if call.Prefetch && call.Size == 2*ChunkSize && call.Offset >= 3*ChunkSize {
				lookaheads = append(lookaheads, call)
			}
		}
		if len(lookaheads) >= 2 {
			if lookaheads[0].Offset == lookaheads[1].Offset {
				t.Fatalf("both lookaheads target the same window: %+v", lookaheads)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lookaheads = %d, want two: %+v", len(lookaheads), r.host.recorded())
		}
		select {
		case <-r.host.changed:
		case <-time.After(time.Millisecond):
		}
	}
}
