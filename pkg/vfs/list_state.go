package vfs

import "sync"

type listState struct {
	loadMu sync.Mutex
	loads  map[string]*listLoad
}

func newListState() *listState {
	return &listState{
		loads: map[string]*listLoad{},
	}
}
