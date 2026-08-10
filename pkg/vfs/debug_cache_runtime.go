package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/vfs/readcache"
)

type vfsDebugCacheRuntime struct {
	v *VFS
}

func newVFSDebugCacheRuntime(v *VFS) vfsDebugCacheRuntime {
	return vfsDebugCacheRuntime{v: v}
}

func (r vfsDebugCacheRuntime) ReadCache() readcache.DebugReadCache {
	return r.v.readCacheSnapshot()
}

func (r vfsDebugCacheRuntime) Journal() *DebugJournal {
	return r.v.uploads.Store().DebugJournal()
}

// DebugReadCacheForTest exposes the read cache debug snapshot with the
// upload journal attached, for tests.
func (c *Stores) DebugReadCacheForTest() DebugCacheSnapshot {
	return DebugCacheSnapshot{
		DebugReadCache: c.readCacheStore.DebugSnapshot(),
		Journal:        c.uploadStore.DebugJournal(),
	}
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
// --- fault injection (registry lives in internal/vfs/faultinject) ---
