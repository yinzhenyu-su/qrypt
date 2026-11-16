package read

import (
	"bytes"
	"testing"
)

func TestSessionTailPageRereadKeepsSequentialRun(t *testing.T) {
	s := NewState(nil)
	observe := func(request uint64, offset, size int64) accessDecision {
		return s.observeReadAccess("file", AccessHint{SessionID: 1, RequestID: request}, offset, size)
	}
	observe(1, 0, ChunkSize)
	sequential := observe(2, ChunkSize, ChunkSize)
	for i, read := range []struct{ offset, size int64 }{
		{2*ChunkSize - 16<<10, 16 << 10}, // repeated last page, no progress
		{2*ChunkSize - 16<<10, 32 << 10}, // overlap extends forward
		{2*ChunkSize + 16<<10, 16 << 10},
	} {
		got := observe(uint64(3+i), read.offset, read.size)
		if !got.sequential || got.discontinuous || got.run != sequential.run {
			t.Fatalf("tail overlap lost the run: %+v", got)
		}
	}
}

func TestTailOverlapDoesNotHideSeeksOrUnidentifiedReaders(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hint         AccessHint
		offset, size int64
	}{
		{"backward_end", AccessHint{SessionID: 1, RequestID: 3}, ChunkSize, 16 << 10},
		{"large_overlap", AccessHint{SessionID: 1, RequestID: 3}, ChunkSize, 2 * ChunkSize},
		{"forward_gap", AccessHint{SessionID: 1, RequestID: 3}, 3 * ChunkSize, 16 << 10},
		{"unidentified", AccessHint{}, 2*ChunkSize - 16<<10, 16 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState(nil)
			first, second := tc.hint, tc.hint
			first.RequestID, second.RequestID = 1, 2
			s.observeReadAccess("file", first, 0, ChunkSize)
			previous := s.observeReadAccess("file", second, ChunkSize, ChunkSize)
			got := s.observeReadAccess("file", tc.hint, tc.offset, tc.size)
			if !got.discontinuous || got.sequential || got.run == previous.run {
				t.Fatalf("seek was hidden: %+v", got)
			}
		})
	}
}

func TestSessionTailPageRereadPreservesLivePrefetch(t *testing.T) {
	r := newStagedReaderWithProfile(t, 16*ChunkSize, slidingProfile(1))
	gate := make(chan struct{})
	defer close(gate)
	// Warm foreground data without starting lookahead, then hold the first
	// speculative window at the provider while the terminal page is reread.
	r.host.gateFn = func(offset, _ int64) (<-chan struct{}, bool) {
		return gate, offset >= 2*ChunkSize
	}
	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.read(t, sessionContext(1, 2), ChunkSize, ChunkSize)
	r.host.waitCall(t, 3)
	p := sessionWorker(t, r, 1)
	got := r.read(t, sessionContext(1, 3), 2*ChunkSize-16<<10, 16<<10)
	if !bytes.Equal(got, r.host.data[2*ChunkSize-16<<10:2*ChunkSize]) {
		t.Fatal("tail page changed bytes")
	}
	if sessionWorker(t, r, 1) != p {
		t.Fatal("tail page retired the playback controller")
	}
}
