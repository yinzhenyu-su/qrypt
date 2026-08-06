package vfs

import "sync"

// pathLockState serializes conflicting operations on the same path.
// Cross-domain (reads, writes, uploads and deletes all take path locks),
// so it stays top-level on VFS rather than inside a domain runtime.
// Lifecycle: created in New, entries are released after each operation.
type pathLockState struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newPathLockState() *pathLockState {
	return &pathLockState{
		locks: map[string]*sync.Mutex{},
	}
}
