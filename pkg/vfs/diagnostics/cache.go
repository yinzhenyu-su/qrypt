package diagnostics

import (
	"github.com/yinzhenyu/qrypt/pkg/vfs/listcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

// CacheRuntime is the read-cache diagnostic surface (consumer side):
// the cache store's own snapshot plus the upload journal for the same
// mount. pkg/vfs implements it with a thin adapter over its stores.
type CacheRuntime interface {
	ReadCache() readcache.DebugReadCache
	Journal() *upload.DebugJournal
	// ListCache returns the persisted directory-listing cache snapshot, or
	// nil when the mount has no list cache configured.
	ListCache() *listcache.Stats
}

// CacheSnapshot assembles the readcache snapshot with the upload journal
// into the flat DebugCacheSnapshot DTO.
func CacheSnapshot(runtime CacheRuntime) DebugCacheSnapshot {
	return DebugCacheSnapshot{
		DebugReadCache: runtime.ReadCache(),
		Journal:        runtime.Journal(),
		ListCache:      runtime.ListCache(),
	}
}
