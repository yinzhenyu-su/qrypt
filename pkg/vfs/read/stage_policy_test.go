package read

// Stage-policy tests: a driver's optional stage suggestions must shape the
// foreground window and lookahead of a read session without changing its
// intent, its stream gate, or its cache admission.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func stageProfile() drive.ReadPolicy {
	profile := drive.DefaultReadPolicy()
	profile.Interactive.Seek = drive.AccessPolicy{Window: drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: ChunkSize}, Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 2 * ChunkSize, Depth: 1}}
	profile.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 2 * ChunkSize, Depth: 2}
	return profile
}

func TestStageSeekSuggestionShrinksFuseForegroundWindow(t *testing.T) {
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
		if call.Size > ChunkSize {
			t.Fatalf("seek range size = %d, want at most one chunk", call.Size)
		}
	}
}

func TestStageSeekSuggestionAppliesToInteractive(t *testing.T) {
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

func TestStageProbeIgnoresStageSuggestion(t *testing.T) {
	profile := stageProfile()

	r := newStagedReaderWithProfile(t, 8<<20, profile)
	ctx := WithReadIntent(sessionContext(1, 2), ReadIntentProbe)
	r.read(t, ctx, 4<<20, 64<<10)
	plan := r.host.lastPlan()
	if plan.Stage != StageInitial && plan.Stage != StageSeek {
		t.Fatalf("probe stage = %v", plan.Stage)
	}
	if plan.WindowChunks != 1 {
		t.Fatalf("probe window = %d, want %d", plan.WindowChunks, 1)
	}
	if plan.PrefetchDepth != 0 || plan.PrefetchChunks != 0 {
		t.Fatalf("probe prefetch = depth %d chunks %d, want none", plan.PrefetchDepth, plan.PrefetchChunks)
	}
	if plan.MaxStreams != 1 {
		t.Fatalf("probe streams = %d, want 1", plan.MaxStreams)
	}
}

func TestStageBulkIgnoresStageSuggestion(t *testing.T) {
	profile := stageProfile()
	profile.Bulk.Streams = 2
	profile.Bulk.Split.MinParallelBytes = 0
	r := newStagedReaderWithProfile(t, 8<<20, profile)
	ctx := WithReadIntent(sessionContext(1, 2), ReadIntentBulk)
	r.read(t, ctx, 4<<20, 64<<10)
	plan := r.host.lastPlan()
	if plan.WindowChunks != byteChunks(profile.Bulk.WindowBytes) {
		t.Fatalf("bulk window = %d, want %d", plan.WindowChunks, byteChunks(profile.Bulk.WindowBytes))
	}
}

func TestStageSequentialSuggestionDrivesLookahead(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, stageProfile())
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	// The confirmed sequential stage prefetches two 2-chunk windows instead of
	// the legacy 16-chunk lookahead.
	lookahead := r.host.waitRangeSize(t, 2*ChunkSize)
	for _, call := range r.host.recorded() {
		if call.Size == int64(SequentialPrefetchChunks)*ChunkSize {
			t.Fatalf("legacy %d-chunk lookahead used despite the stage suggestion", SequentialPrefetchChunks)
		}
	}
	// Assert on the lookahead call itself: other (earlier-plan) Ranges may be
	// recorded after it, so the host's last plan is not a reliable witness.
	if lookahead.Plan.Stage != StageSequential {
		t.Fatalf("lookahead stage = %v, want sequential after a confirmed run", lookahead.Plan.Stage)
	}
}

func TestStageExplicitZeroDepthDisablesLookahead(t *testing.T) {
	profile := stageProfile()
	profile.Interactive.Sequential = drive.AccessPolicy{Window: drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: ChunkSize}, Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchDisabled}}
	r := newStagedReaderWithProfile(t, 64<<20, profile)
	// The initial stage still inherits the intent default, so its 1-chunk
	// windows are expected. Once the run is confirmed, the sequential stage's
	// explicit zero must stop every further lookahead: neither the 2-chunk
	// stage window nor the legacy 16-chunk window may appear.
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.read(t, sessionContext(1, 3), 2*ChunkSize, ChunkSize)
	time.Sleep(100 * time.Millisecond)
	for _, call := range r.host.recorded() {
		if call.Size > 2*ChunkSize {
			t.Fatalf("lookahead ran despite depth zero: %+v", call)
		}
		if call.Size == 2*ChunkSize {
			t.Fatalf("sequential stage prefetched despite depth zero: %+v", call)
		}
	}
}

// A stage may name only a depth. The legacy window-size rule still applies,
// but the suggested depth then bounds how many windows it opens; before, the
// depth was ignored unless an explicit prefetch window accompanied it.
func TestStageDepthOnlySuggestionBoundsLegacyLookahead(t *testing.T) {
	cases := []struct {
		name         string
		depth        int
		windowChunks int
	}{
		{"narrow_window_depth_one", 1, 1},
		{"wide_window_depth_three", 3, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := drive.DefaultReadPolicy()
			profile.Bulk.Streams = 1
			profile.Interactive.Sequential.Prefetch = drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: int64(map[bool]int{true: SequentialPrefetchChunks, false: tc.windowChunks}[tc.windowChunks == 1]) * ChunkSize, Depth: tc.depth}
			r := newStagedReaderWithProfile(t, 64<<20, profile)

			release := make(chan struct{})
			defer close(release)
			r.host.gateFn = blockEveryRange(release)

			plan := PlanResolver{Profile: profile}.ResolveStage(StageInitial, ReadIntentUnspecified, tc.windowChunks)
			r.reader.schedulePrefetch(context.Background(), r.host.entry, 0, tc.windowChunks, true, plan, deliveredRange{})

			// Window registration happens before the fetch starts, so the count
			// is exact even while every window is blocked on the provider.
			windows, prefetches, _ := r.state.RuntimeStats()
			if prefetches != tc.depth || windows != tc.depth {
				t.Fatalf("lookahead windows = %d (permits %d), want %d", windows, prefetches, tc.depth)
			}
			// Sequential one-chunk reads widen to the sequential lookahead
			// window; a wider read keeps its own width. Either way the depth
			// decides how many of them exist.
			window := int64(tc.windowChunks) * ChunkSize
			if tc.windowChunks == 1 {
				window = int64(SequentialPrefetchChunks) * ChunkSize
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				var calls []stagedCall
				for _, call := range r.host.recorded() {
					if call.Prefetch {
						calls = append(calls, call)
					}
				}
				if len(calls) >= tc.depth {
					// The windows are fetched concurrently, so compare the set
					// of offsets rather than their issue order.
					got := make(map[int64]int64, len(calls))
					for _, call := range calls {
						got[call.Offset] = call.Size
					}
					for i := 0; i < tc.depth; i++ {
						wantOffset := ChunkSize + int64(i)*window
						if size, ok := got[wantOffset]; !ok || size != window {
							t.Fatalf("lookahead windows = %v, want offset %d size %d", got, wantOffset, window)
						}
					}
					if len(got) != tc.depth {
						t.Fatalf("lookahead windows = %v, want %d distinct windows", got, tc.depth)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("lookahead calls = %d, want %d: %+v", len(calls), tc.depth, calls)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestStagePrefetchDisabledBeatsSuggestion(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	ctx := WithoutReadPrefetch(sessionContext(1, 1))
	r.read(t, ctx, 0, ChunkSize)
	time.Sleep(50 * time.Millisecond)
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
	if len(cache.puts) == 0 {
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
	profile.Interactive.Sequential = drive.AccessPolicy{Window: drive.WindowPolicy{Mode: drive.WindowFixed, Bytes: 2 * ChunkSize}, Prefetch: drive.PrefetchPolicy{Mode: drive.PrefetchFixed, WindowBytes: 2 * ChunkSize, Depth: 2}}
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
		time.Sleep(time.Millisecond)
	}
}
