package vfs

import "sync"

// listState coalesces concurrent List calls for the same directory.
// Owned by the read domain (readState.list); guarded by its own mutex.
// Lifecycle: created in newReadState, entries expire with the listing
// TTL or are invalidated by list invalidation.
type listState struct {
	loadMu sync.Mutex
	loads  map[string]*listLoad
}

func newListState() *listState {
	return &listState{
		loads: map[string]*listLoad{},
	}
}
