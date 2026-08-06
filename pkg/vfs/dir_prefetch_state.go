package vfs

import (
	"context"
	"sync"
	"time"
)

// dirPrefetchState tracks the in-flight directory prefetch worker.
// Owned by the listing domain (listingState.dirPrefetch); guarded by its
// own mutex. Lifecycle: created in newListingState, started/stopped with
// the VFS context.
type dirPrefetchState struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	done     map[string]time.Time
	sem      chan struct{}
	context  context.Context
	started  bool
}

func newDirPrefetchState() *dirPrefetchState {
	return &dirPrefetchState{
		inFlight: map[string]struct{}{},
		done:     map[string]time.Time{},
		sem:      make(chan struct{}, dirPrefetchLimit),
	}
}
