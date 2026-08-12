package vfs

import (
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
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

func (r vfsDebugCacheRuntime) Journal() *upload.DebugJournal {
	return r.v.uploads.Store().DebugJournal()
}

// debugActiveSlots is the fixed capacity of the active-debug ring. Active
// operations are short-lived (microseconds), so 128 concurrent ops is a
// generous bound; when full, Begin returns 0 (tracking skipped).
// --- fault injection (registry lives in pkg/vfs/faultinject) ---
