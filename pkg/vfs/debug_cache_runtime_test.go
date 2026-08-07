package vfs

import (
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/internal/upload"
)

type fakeDebugCacheRuntime struct {
	cache   readcache.DebugReadCache
	journal *upload.DebugJournal
}

func (r fakeDebugCacheRuntime) ReadCache() readcache.DebugReadCache {
	return r.cache
}

func (r fakeDebugCacheRuntime) Journal() *upload.DebugJournal {
	return r.journal
}

func TestDebugCacheSnapshotUsesRuntime(t *testing.T) {
	journal := &upload.DebugJournal{Path: "pending.jsonl", PendingCount: 2}
	snapshot := debugCacheSnapshotWithRuntime(fakeDebugCacheRuntime{
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
}
