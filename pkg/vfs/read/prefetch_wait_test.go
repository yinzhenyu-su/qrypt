package read

// A speculative window with no useful target progress must permit bounded
// foreground takeover. A received block can be reused without waiting for its
// lookahead tail; paced-body tests cover progress slower than the wait budget.

import (
	"bytes"
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

// wideLookaheadProfile keeps the quark-like single-stream shape but makes the
// confirmed sequential stage issue one speculative window wider than the
// foreground's chunk, which these tests hold open.
func wideLookaheadProfile() drive.ReadPolicy {
	profile := quarkLikeProfile()
	profile.Interactive.PrefetchWindowBytes = 2 * ChunkSize
	profile.Interactive.PrefetchDepth = 1
	return profile
}

// waitForWideLookahead returns the first speculative Range wider than the
// foreground's single chunk, which is the window these tests hold open.
func waitForWideLookahead(t *testing.T, r *stagedReader, offset int64) stagedCall {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, call := range r.host.recorded() {
			if call.Prefetch && call.Size > ChunkSize && call.Offset == offset {
				return call
			}
		}
		select {
		case <-r.host.changed:
		case <-deadline:
			t.Fatalf("no wide lookahead at %d issued: %+v", offset, r.host.recorded())
		}
	}
}

// establishSequentialRun reads two contiguous chunks without prefetch, so the
// next read both confirms the run and starts the wide lookahead.
func establishSequentialRun(t *testing.T, r *stagedReader) {
	t.Helper()
	r.read(t, WithoutReadPrefetch(sessionContext(1, 1)), 0, ChunkSize)
	r.read(t, WithoutReadPrefetch(sessionContext(1, 2)), ChunkSize, ChunkSize)
}

func TestFastPrefetchWindowIsStillJoined(t *testing.T) {
	r := newStagedReaderWithProfile(t, 64<<20, wideLookaheadProfile())
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

func TestFuseSequentialUpgradeKeepsExplicitPrefetchDepth(t *testing.T) {
	profile := drive.DefaultReadPolicy()
	profile.Bulk.Streams = 3
	profile.Interactive.PrefetchWindowBytes = 2 * PrefetchChunks * ChunkSize
	profile.Interactive.PrefetchDepth = 1
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
	window := int64(2*PrefetchChunks) * ChunkSize
	call := r.host.waitRangeSize(t, window)
	windows, prefetches, _ := r.state.RuntimeStats()
	if windows != 1 || prefetches != 1 {
		t.Fatalf("lookahead windows = %d (permits %d), want the single suggested window", windows, prefetches)
	}
	if !call.Prefetch || call.Offset != 3*ChunkSize {
		t.Fatalf("lookahead call = %+v, want one speculative %d-chunk window at chunk 3", call, 2*PrefetchChunks)
	}
}
