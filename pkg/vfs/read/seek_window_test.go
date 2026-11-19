package read

// A seek must be able to deliver its own small window even when a much larger
// speculative window for the same file is already in flight. Waiting for the
// whole lookahead would make the seek pay for data it never asked for; instead
// the seek loads its own range and the speculative window keeps running.

import (
	"bytes"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func TestSeekDoesNotWaitForOversizedPrefetchWindow(t *testing.T) {
	// A fixed sequential stage suggestion makes the speculative lookahead one
	// wide window, which is the in-flight range this test gates.
	profile := drive.DefaultReadPolicy()
	profile.Interactive.PrefetchWindowBytes = 8 << 20
	profile.Interactive.PrefetchDepth = 1
	// Isolate window ownership from budget saturation: four lookaheads leave
	// capacity for the other session's foreground request.
	profile.MaxInFlight = 8
	r := newStagedReaderWithProfile(t, 64<<20, profile)
	release := make(chan struct{})
	defer close(release)
	// Gate every speculative window wider than the seek's own window and never
	// release it while the seek runs.
	r.host.gateFn = func(_, size int64) (<-chan struct{}, bool) {
		if size > 2*ChunkSize {
			return release, true
		}
		return nil, false
	}

	// Session 2 confirms a sequential run, which admits the wide lookahead.
	// Read its start from the live window registry rather than the recorded
	// calls: the provider call is appended asynchronously, so a call recorded
	// from an earlier (already retired) lookahead would point the seek at a
	// later window's head block.
	wideStart := int64(-1)
	for chunk := int64(0); chunk < 8 && wideStart < 0; chunk++ {
		r.read(t, sessionContext(2, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
		if start, ok := wideLookaheadStart(r); ok {
			wideStart = start
		}
	}
	if wideStart < 0 {
		t.Fatalf("no wide lookahead issued: %+v", r.host.recorded())
	}

	// Session 1 seeks into that wide window, at a block it covers but does not
	// head: another session's lookahead may only be joined for its head block,
	// so this seek must load its own window instead of waiting for the gated
	// range.
	seekOffset := wideStart*ChunkSize + ChunkSize
	done := r.goRead(sessionContext(1, 1), seekOffset, ChunkSize)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("seek read: %v", result.err)
		}
		if !bytes.Equal(result.data, r.host.data[seekOffset:seekOffset+ChunkSize]) {
			t.Fatal("seek returned wrong bytes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("seek waited for the oversized in-flight prefetch window")
	}

	// The seek fetched its own aligned one-chunk range.
	var ownRange bool
	for _, call := range r.host.recorded() {
		if call.Offset == seekOffset && call.Size == ChunkSize {
			ownRange = true
		}
	}
	if !ownRange {
		t.Fatalf("seek did not issue its own small range: %+v", r.host.recorded())
	}
	// The speculative window is untouched: its gate was never released.
	for _, call := range r.host.recorded() {
		if call.Size > 2*ChunkSize && call.Err != nil {
			t.Fatalf("wide prefetch was canceled by an unrelated seek: %v", call.Err)
		}
	}
}

// wideLookaheadStart returns the first chunk of a live speculative window that
// is wider than the seek's own window. Reading the live registry keeps the test
// independent of when the asynchronous fetch records its provider call.
func wideLookaheadStart(r *stagedReader) (int64, bool) {
	r.reader.state.windows.mu.Lock()
	defer r.reader.state.windows.mu.Unlock()
	for _, load := range r.reader.state.windows.loads {
		if !load.prefetch || (load.end-load.start+1)*ChunkSize <= 2*ChunkSize {
			continue
		}
		select {
		case <-load.done:
			continue
		default:
		}
		return load.start, true
	}
	return 0, false
}

func TestSeekStillWaitsForCompatibleWindow(t *testing.T) {
	r := newStagedReaderWithProfile(t, 8<<20, stageProfile())
	// A two-chunk in-flight window is exactly the seek's own merged window, so
	// waiting for it is correct and avoids a duplicate download.
	release := make(chan struct{})
	const windowOffset = 2 * ChunkSize
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == windowOffset {
			return release, true
		}
		return nil, false
	}
	// Session 1's first read of the merged 2-chunk window is gated.
	done := r.goRead(sessionContext(1, 1), windowOffset+1000, 64<<10)
	r.host.waitRangeSize(t, 2*ChunkSize)

	// A second seek into the same window must join it rather than duplicate it.
	second := r.goRead(sessionContext(1, 2), windowOffset+2000, 64<<10)
	select {
	case <-second:
		t.Fatal("second seek did not join the compatible in-flight window")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, ch := range []<-chan asyncRead{done, second} {
		select {
		case result := <-ch:
			if result.err != nil {
				t.Fatalf("seek read: %v", result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("seek did not finish after the window was released")
		}
	}
	var merged int
	for _, call := range r.host.recorded() {
		if call.Offset == windowOffset && call.Size == 2*ChunkSize {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("merged window fetched %d times, want one shared fetch", merged)
	}
}
