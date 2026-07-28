package vfs

import (
	"context"
	"sync"
	"time"
)

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
