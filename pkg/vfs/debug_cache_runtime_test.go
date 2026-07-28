package vfs

import "testing"

type fakeDebugCacheRuntime struct {
	cache   DebugReadCache
	journal *DebugJournal
}

func (r fakeDebugCacheRuntime) ReadCache() DebugReadCache {
	return r.cache
}

func (r fakeDebugCacheRuntime) Journal() *DebugJournal {
	return r.journal
}

func TestDebugCacheSnapshotUsesRuntime(t *testing.T) {
	journal := &DebugJournal{Path: "pending.jsonl", PendingCount: 2}
	snapshot := debugCacheSnapshotWithRuntime(fakeDebugCacheRuntime{
		cache: DebugReadCache{
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
