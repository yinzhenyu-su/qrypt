package read

import (
	"testing"
	"time"
)

// A seek must drop only the lookahead of the session that jumped. Another open
// handle reading the same file keeps its own speculative window; canceling it
// would turn one player's seek into the other's stall.
func TestSeekCancelsOnlyItsOwnSessionLookahead(t *testing.T) {
	r := newStagedReader(t, 64<<20)
	release := make(chan struct{})
	defer close(release)
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		switch offset {
		case ChunkSize: // session 1 lookahead
			return release, true
		case 42 * ChunkSize: // session 2 lookahead
			return release, true
		}
		return nil, false
	}

	// Session 1 opens and leaves a gated lookahead.
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitRangeSize(t, ChunkSize)

	// Session 2 opens at another offset and confirms a sequential run, which
	// leaves its own gated lookahead.
	r.read(t, sessionContext(2, 1), 40*ChunkSize, ChunkSize)
	r.read(t, sessionContext(2, 2), 41*ChunkSize, ChunkSize)
	deadline := time.Now().Add(2 * time.Second)
	for {
		gated := 0
		for _, call := range r.host.recorded() {
			if call.Gated {
				gated++
			}
		}
		if gated >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second session lookahead did not start: %+v", r.host.recorded())
		}
		time.Sleep(time.Millisecond)
	}

	// Session 1 seeks: only its own gated lookahead may be canceled.
	r.read(t, sessionContext(1, 2), 20*ChunkSize, ChunkSize)

	deadline = time.Now().Add(2 * time.Second)
	for {
		var s1Canceled, s2Canceled bool
		for _, call := range r.host.recorded() {
			if call.Offset == ChunkSize && call.Err != nil {
				s1Canceled = true
			}
			if call.Offset == 42*ChunkSize && call.Err != nil {
				s2Canceled = true
				t.Logf("session 2 lookahead ended with %v", call.Err)
			}
		}
		if s2Canceled {
			t.Fatalf("session 1's seek canceled session 2's lookahead: %+v", r.host.recorded())
		}
		if s1Canceled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session 1's seek did not cancel its own lookahead: %+v", r.host.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}
