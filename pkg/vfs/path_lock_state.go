package vfs

import "sync"

type pathLockState struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newPathLockState() *pathLockState {
	return &pathLockState{
		locks: map[string]*sync.Mutex{},
	}
}
