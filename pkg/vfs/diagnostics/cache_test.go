package diagnostics

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/listcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/upload"
)

type fakeCacheRuntime struct {
	cache     readcache.DebugReadCache
	journal   *upload.DebugJournal
	listCache *listcache.Stats
}

func (r fakeCacheRuntime) ReadCache() readcache.DebugReadCache {
	return r.cache
}

func (r fakeCacheRuntime) Journal() *upload.DebugJournal {
	return r.journal
}

func (r fakeCacheRuntime) ListCache() *listcache.Stats {
	return r.listCache
}

func TestCacheSnapshotUsesRuntime(t *testing.T) {
	journal := &upload.DebugJournal{Path: "pending.jsonl", PendingCount: 2}
	snapshot := CacheSnapshot(fakeCacheRuntime{
		cache: readcache.DebugReadCache{
			Hits:       3,
			Misses:     4,
			ChunkCount: 5,
		},
		journal: journal,
	})

	if snapshot.Hits != 3 || snapshot.Misses != 4 || snapshot.ChunkCount != 5 {
		t.Fatalf("cache fields = %+v", snapshot)
	}
	if snapshot.Journal != journal {
		t.Fatalf("journal = %+v, want %+v", snapshot.Journal, journal)
	}
	if snapshot.ListCache != nil {
		t.Fatalf("list cache = %+v, want nil", snapshot.ListCache)
	}
}
