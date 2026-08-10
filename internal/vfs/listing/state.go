package listing

import (
	"context"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	listCacheTTL        = 10 * time.Second
	dirPrefetchLimit    = 2
	dirPrefetchCooldown = 5 * time.Minute
	dirPrefetchTimeout  = 15 * time.Second
)

type listCacheEntry struct {
	entries []drive.Entry
	expires time.Time
}

// listLoad coalesces concurrent List calls for the same directory.
type listLoad struct {
	done     chan struct{}
	entries  []drive.Entry
	err      error
	prefetch bool
}

// listState coalesces concurrent List calls for the same directory. Owned
// by the listing domain; guarded by loadMu. Lifecycle: created in
// NewState, entries expire with the listing TTL or are invalidated.
type listState struct {
	loadMu sync.Mutex
	loads  map[string]*listLoad
}

func newListState() *listState {
	return &listState{
		loads: map[string]*listLoad{},
	}
}

// dirPrefetchState tracks the in-flight directory prefetch worker. Owned
// by the listing domain; guarded by its own mutex. Lifecycle: created in
// NewState, started/stopped with the VFS context.
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

// State groups the directory-listing domain state: list coalescing and
// directory prefetch. It is separate from the read domain because it
// serves directory browsing (List) rather than file content (Read). The
// domain holds no persistent resources, so there is no Close - directory
// prefetch runs on the VFS lifecycle context and stops with it.
type State struct {
	list        *listState
	dirPrefetch *dirPrefetchState
}

// NewState builds the listing domain state together.
func NewState() *State {
	return &State{
		list:        newListState(),
		dirPrefetch: newDirPrefetchState(),
	}
}

// BeginListLoad returns the in-flight load for parentPath, or registers a
// new one and reports ownership.
func (s *State) BeginListLoad(parentPath string, prefetch bool) (*listLoad, bool) {
	s.list.loadMu.Lock()
	defer s.list.loadMu.Unlock()
	if load := s.list.loads[parentPath]; load != nil {
		return load, false
	}
	load := &listLoad{done: make(chan struct{}), prefetch: prefetch}
	s.list.loads[parentPath] = load
	return load, true
}

// FinishListLoad completes a load, waking waiters.
func (s *State) FinishListLoad(parentPath string, load *listLoad, entries []drive.Entry, err error) {
	if err == nil {
		load.entries = cloneEntries(entries)
	}
	load.err = err
	s.list.loadMu.Lock()
	if s.list.loads[parentPath] == load {
		delete(s.list.loads, parentPath)
	}
	s.list.loadMu.Unlock()
	close(load.done)
}

// MarkDirPrefetch reserves the prefetch slot for a directory.
func (s *State) MarkDirPrefetch(path string, fresh bool) bool {
	if fresh {
		return false
	}
	now := time.Now()
	s.dirPrefetch.mu.Lock()
	defer s.dirPrefetch.mu.Unlock()
	if _, ok := s.dirPrefetch.inFlight[path]; ok {
		return false
	}
	if last, ok := s.dirPrefetch.done[path]; ok && now.Sub(last) < dirPrefetchCooldown {
		return false
	}
	s.dirPrefetch.inFlight[path] = struct{}{}
	return true
}

// MarkDirPrefetchComplete records the completion time of a prefetch.
func (s *State) MarkDirPrefetchComplete(path string) {
	s.dirPrefetch.mu.Lock()
	s.dirPrefetch.done[path] = time.Now()
	s.dirPrefetch.mu.Unlock()
}

// SuppressDirPrefetch marks a directory as recently prefetched.
func (s *State) SuppressDirPrefetch(path string) {
	s.dirPrefetch.mu.Lock()
	s.dirPrefetch.done[path] = time.Now()
	s.dirPrefetch.mu.Unlock()
}

// FinishDirPrefetch releases the in-flight reservation.
func (s *State) FinishDirPrefetch(path string) {
	s.dirPrefetch.mu.Lock()
	delete(s.dirPrefetch.inFlight, path)
	s.dirPrefetch.mu.Unlock()
}

// AcquireDirPrefetchSlot bounds concurrent prefetch workers.
func (s *State) AcquireDirPrefetchSlot(ctx context.Context) bool {
	select {
	case s.dirPrefetch.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// ReleaseDirPrefetchSlot releases a prefetch worker slot.
func (s *State) ReleaseDirPrefetchSlot() {
	<-s.dirPrefetch.sem
}

// StartDirPrefetch records the lifecycle context for background prefetch.
func (s *State) StartDirPrefetch(ctx context.Context) bool {
	s.dirPrefetch.mu.Lock()
	defer s.dirPrefetch.mu.Unlock()
	if s.dirPrefetch.started {
		return false
	}
	s.dirPrefetch.started = true
	s.dirPrefetch.context = ctx
	return true
}

// DirPrefetchContext returns the lifecycle context when healthy.
func (s *State) DirPrefetchContext(fallback context.Context) context.Context {
	s.dirPrefetch.mu.Lock()
	ctx := s.dirPrefetch.context
	s.dirPrefetch.mu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		return ctx
	}
	return fallback
}

func cloneEntries(entries []drive.Entry) []drive.Entry {
	return append([]drive.Entry(nil), entries...)
}

// StatesReady reports whether the sub-states are initialized.
func (s *State) StatesReady() bool {
	return s != nil && s.list != nil && s.dirPrefetch != nil
}
