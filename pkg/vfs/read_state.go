package vfs

import "sync"

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

type readWindowState struct {
	mu    sync.Mutex
	loads map[string]*windowLoad
}

func newReadWindowState() *readWindowState {
	return &readWindowState{
		loads: map[string]*windowLoad{},
	}
}

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
	cache       *readCacheStore
	history     *readHistoryState
	prefetch    *readPrefetchState
	slots       *readSlotState
	fastPath    *readFastPathState
	windows     *readWindowState
	dirPrefetch *dirPrefetchState
	list        *listState
}
