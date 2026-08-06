package vfs

import "sync"

// readSlotState bounds concurrent driver reads per VFS. Owned by the read
// domain (readWindowState/readRuntime); the normal/high channels are the
// lock itself - no additional mutex. Lifecycle: created in newReadState,
// never closed (workers select on the VFS context instead).
type readSlotState struct {
	normal chan struct{}
	high   chan struct{}
}

func newReadSlotState() *readSlotState {
	return &readSlotState{
		normal: make(chan struct{}, readMaxConcurrency-readHighReserve),
		high:   make(chan struct{}, readHighReserve),
	}
}

// readWindowState coalesces concurrent reads of the same cache window.
// Owned by the read domain; mu guards loads. Lifecycle: created in
// newReadState, entries are removed when their window load finishes.
type readWindowState struct {
	mu    sync.Mutex
	loads map[string]*windowLoad
}

func newReadWindowState() *readWindowState {
	return &readWindowState{
		loads: map[string]*windowLoad{},
	}
}

// hotChunkState is the in-memory hot-chunk cache (fast path around the
// durable read cache). Owned by the read domain via readFastPathState; mu
// guards chunks/lru. Lifecycle: created in newReadFastPathState, bounded
// by the fast-path entry budget.
type hotChunkState struct {
	mu     sync.Mutex
	chunks map[string][]byte
	lru    []string
}

type rangeHitState struct {
	mu   sync.Mutex
	hits map[string]int
	lru  []string
}

type readFastPathState struct {
	hot      hotChunkState
	rangeHit rangeHitState
}

func newReadFastPathState() *readFastPathState {
	return &readFastPathState{
		hot: hotChunkState{
			chunks: map[string][]byte{},
		},
		rangeHit: rangeHitState{
			hits: map[string]int{},
		},
	}
}

type readPrefetchState struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	sem      chan struct{}
}

func newReadPrefetchState() *readPrefetchState {
	return &readPrefetchState{
		inFlight: map[string]struct{}{},
		sem:      make(chan struct{}, readPrefetchLimit),
	}
}

// readState groups the VFS read-domain state so ownership is explicit:
// the fields are initialized together in New, touched only by read paths,
// and shut down by the VFS lifecycle.
type readState struct {
	cache    *readCacheStore
	history  *readHistoryState
	prefetch *readPrefetchState
	slots    *readSlotState
	fastPath *readFastPathState
	windows  *readWindowState
}

// newReadState builds the read domain state together so ownership and
// initialization stay in one place. cache is the durable read-cache store
// (nil-safe: read paths fall back to no caching).
func newReadState(cache *readCacheStore) *readState {
	return &readState{
		cache:    cache,
		history:  newReadHistoryState(),
		prefetch: newReadPrefetchState(),
		slots:    newReadSlotState(),
		fastPath: newReadFastPathState(),
		windows:  newReadWindowState(),
	}
}

// Close stops the durable read-cache writer and waits for pending writes.
// Called by the VFS lifecycle. Safe on a zero readState (hand-constructed
// VFS values in tests may have no cache).
func (r *readState) Close() error {
	if r == nil || r.cache == nil {
		return nil
	}
	return r.cache.Close()
}
