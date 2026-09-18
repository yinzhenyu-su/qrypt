package read

import (
	"context"
	"io"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
)

// recordingCache is a Cache that keeps the Access each call arrived with. The
// read domain's job here is only to carry the read run from the read context to
// the cache; whether a run then promotes a chunk is the cache's business and is
// covered where the policy lives.
type recordingCache struct {
	puts   []readcache.Access
	lookup []readcache.Access
}

func (c *recordingCache) AddHit()                  {}
func (c *recordingCache) Close() error             { return nil }
func (c *recordingCache) Counters() (int64, int64) { return 0, 0 }
func (c *recordingCache) FlushReadCache() error    { return nil }
func (c *recordingCache) ClearReadCache() error    { return nil }
func (c *recordingCache) InvalidateFile(string)    {}
func (c *recordingCache) PutLocalFile(string, int64, string) error {
	return nil
}
func (c *recordingCache) PutReader(string, int64, io.Reader) error { return nil }
func (c *recordingCache) HasChunk(string, int64) (bool, error)     { return false, nil }
func (c *recordingCache) DebugSnapshot() readcache.DebugReadCache  { return readcache.DebugReadCache{} }

func (c *recordingCache) GetChunkRange(_ string, _, _, _ int64, access readcache.Access) ([]byte, bool, error) {
	c.lookup = append(c.lookup, access)
	return nil, false, nil
}

func (c *recordingCache) GetChunkWithRange(_ string, _, _, _ int64, access readcache.Access) ([]byte, []byte, bool, error) {
	c.lookup = append(c.lookup, access)
	return nil, nil, false, nil
}

func (c *recordingCache) PutChunkAsync(_ string, _, _ int64, _ []byte, access readcache.Access) {
	c.puts = append(c.puts, access)
}

// TestStoreChunkCarriesTheRunFromContext pins the write half of the plumbing: a
// window fetch admitted under a read context must hand the cache that context's
// run, otherwise the cache cannot tell the stream's own read-ahead from reuse.
func TestStoreChunkCarriesTheRunFromContext(t *testing.T) {
	cache := &recordingCache{}
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(cache)})
	backend := reader.newBackend()

	ctx := WithCacheAccess(context.Background(), readcache.Access{Run: 42})
	backend.StoreChunk(ctx, "cache-key", drive.Entry{ID: "id"}, 3, []byte("chunk"))

	if len(cache.puts) != 1 {
		t.Fatalf("cache received %d puts, want 1", len(cache.puts))
	}
	if got := cache.puts[0].Run; got != 42 {
		t.Fatalf("admitted run = %d, want 42", got)
	}
}

// TestReadChunkRangeCarriesTheRunFromContext pins the read half.
func TestReadChunkRangeCarriesTheRunFromContext(t *testing.T) {
	runtime := &fakeReadRuntime{cacheKey: "cache-key"}
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})

	ctx := WithCacheAccess(context.Background(), readcache.Access{Run: 7})
	if _, err := reader.readChunkRangeWithRuntime(ctx, drive.Entry{ID: "id", Size: ChunkSize}, 0, 0, 16, 1, runtime); err != nil {
		t.Fatal(err)
	}
	if len(runtime.lookups) == 0 {
		t.Fatal("the runtime performed no cache lookup")
	}
	for _, access := range runtime.lookups {
		if access.Run != 7 {
			t.Fatalf("lookup run = %d, want 7", access.Run)
		}
	}
}

// TestStoreChunkWithoutCacheAccessAdmitsAsUnknownRun documents the failure mode
// of a missing WithCacheAccess: the cache sees a zero run, which it treats as
// unknown and therefore as reuse. Retention degrades to "promote on first hit",
// which is the pre-existing behaviour rather than a wrong answer.
func TestStoreChunkWithoutCacheAccessAdmitsAsUnknownRun(t *testing.T) {
	cache := &recordingCache{}
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(cache)})

	reader.newBackend().StoreChunk(context.Background(), "cache-key", drive.Entry{ID: "id"}, 0, []byte("chunk"))

	if len(cache.puts) != 1 || cache.puts[0] != (readcache.Access{}) {
		t.Fatalf("admitted access = %+v, want the zero Access", cache.puts)
	}
}
