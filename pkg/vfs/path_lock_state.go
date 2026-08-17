package vfs

import "sync"

// pathLockState serializes conflicting operations on the same path.
// Cross-domain (reads, writes, uploads and deletes all take path locks),
// so it stays top-level on VFS rather than inside a domain runtime.
// Lifecycle: created in New, entries are released after each operation
// (the map entry is deleted once its last holder unlocks, so long-running
// mounts do not accumulate one lock per path ever touched).
type pathLockState struct {
	// mu guards the locks map and each entry's refcount. It is only held
	// for map/refcount bookkeeping, never while a path mutex is locked.
	mu    sync.Mutex
	locks map[string]*pathLockEntry
}

type pathLockEntry struct {
	mu   sync.Mutex
	refs int // guarded by pathLockState.mu
}

func newPathLockState() *pathLockState {
	return &pathLockState{locks: make(map[string]*pathLockEntry)}
}

// lock acquires the path mutex and returns its unlock func. Entries are
// reference-counted: acquisition and release run under the state mutex, so
// an entry can only be removed from the map when no holder or waiter can
// still reach it (mutual exclusion is never lost to entry recycling).
func (s *pathLockState) lock(path string) func() {
	s.mu.Lock()
	e, ok := s.locks[path]
	if !ok {
		e = &pathLockEntry{}
		s.locks[path] = e
	}
	e.refs++
	s.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		s.mu.Lock()
		e.refs--
		if e.refs == 0 && s.locks[path] == e {
			delete(s.locks, path)
		}
		s.mu.Unlock()
	}
}
