package readcache

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T, cacheDir string, size int64) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(cacheDir, "reading"), size)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestReadCachePersistsBatchIndex(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)

	c1 := newTestStore(t, cacheDir, 10<<20)
	if err := c1.PutChunk(key, int64(len("cached")), 0, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	if err := c1.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}

	c2 := newTestStore(t, cacheDir, 10<<20)
	got, ok, err := c2.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("cached chunk = %q ok=%v, want cached", got, ok)
	}
}
func TestReadCacheCleansStaleIndexTempOnStartup(t *testing.T) {
	cacheDir := t.TempDir()
	readingDir := filepath.Join(cacheDir, "reading")
	if err := os.MkdirAll(readingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpPath := filepath.Join(readingDir, "index.json.tmp")
	if err := os.WriteFile(tmpPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	newTestStore(t, cacheDir, 10<<20)
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("stale temp index still exists, err=%v", err)
	}
}
func TestReadCacheGetsChunkRange(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	copy(chunk[32:48], []byte("0123456789abcdef"))
	cache := newTestStore(t, cacheDir, 10<<20)
	if err := cache.PutChunk(key, int64(len(chunk)), 0, chunk); err != nil {
		t.Fatal(err)
	}

	got, ok, err := cache.GetChunkRange(key, 0, 32, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("range cache lookup missed")
	}
	if string(got) != "0123456789abcdef" {
		t.Fatalf("range cache data = %q", got)
	}
	state := cache.DebugSnapshot()
	if state.Hits != 1 || state.Misses != 0 {
		t.Fatalf("cache stats = hits %d misses %d, want hits 1 misses 0", state.Hits, state.Misses)
	}
}
func TestReadCacheRangeTreatsMissingBatchAsMiss(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	if err := cache.PutChunk(key, int64(len("cached")), 0, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cacheDir, "reading", "*.batch"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("batch files = %v, want one", matches)
	}
	if err := os.Remove(matches[0]); err != nil {
		t.Fatal(err)
	}

	got, ok, err := cache.GetChunkRange(key, 0, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	if ok || got != nil {
		t.Fatalf("missing batch range = %q ok=%v, want miss", got, ok)
	}
	if has, err := cache.HasChunk(key, 0); err != nil {
		t.Fatal(err)
	} else if has {
		t.Fatal("stale chunk index was not removed")
	}
}
func TestReadCachePutRecreatesReadingDir(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	if err := os.RemoveAll(filepath.Join(cacheDir, "reading")); err != nil {
		t.Fatal(err)
	}

	if err := cache.PutChunk(key, int64(len("cached")), 0, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	if err := cache.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("cached chunk = %q ok=%v, want cached", got, ok)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "reading", "index.json")); err != nil {
		t.Fatal(err)
	}
}
func TestReadCacheClearRemovesReadingData(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("b", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	t.Cleanup(func() { _ = cache.Close() })

	if err := cache.PutChunk(key, int64(len("cached")), 0, []byte("cached")); err != nil {
		t.Fatal(err)
	}
	if err := cache.FlushReadCache(); err != nil {
		t.Fatal(err)
	}
	if state := cache.DebugSnapshot(); state.Bytes == 0 || state.ChunkCount == 0 {
		t.Fatalf("cache not populated before clear: %+v", state)
	}

	if err := cache.ClearReadCache(); err != nil {
		t.Fatal(err)
	}
	state := cache.DebugSnapshot()
	if state.Bytes != 0 || state.ChunkCount != 0 || state.FileCount != 0 {
		t.Fatalf("cache not cleared: %+v", state)
	}
	if entries, err := os.ReadDir(filepath.Join(cacheDir, "reading")); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("reading dir entries after clear = %d, want 0", len(entries))
	}
}
func TestReadCacheAsyncPutRecreatesReadingDir(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	if err := os.RemoveAll(filepath.Join(cacheDir, "reading")); err != nil {
		t.Fatal(err)
	}

	cache.PutChunkAsync(key, int64(len("cached")), 0, []byte("cached"))
	cache.WaitReadCacheWrites()
	if err := cache.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("cached chunk = %q ok=%v, want cached", got, ok)
	}
}
func TestReadCacheAsyncPutSkipsExistingAndPendingChunks(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)

	cache.PutChunkAsync(key, int64(len("cached")), 0, []byte("cached"))
	cache.PutChunkAsync(key, int64(len("cached")), 0, []byte("cached"))
	cache.WaitReadCacheWrites()
	if err := cache.FlushReadIndex(); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("cached chunk = %q ok=%v, want cached", got, ok)
	}
	state := cache.DebugSnapshot()
	if state.Puts != 1 {
		t.Fatalf("puts = %d, want 1", state.Puts)
	}

	cache.PutChunkAsync(key, int64(len("cached")), 0, []byte("new"))
	cache.WaitReadCacheWrites()
	got, ok, err = cache.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("cached chunk after duplicate async put = %q ok=%v, want cached", got, ok)
	}
	state = cache.DebugSnapshot()
	if state.Puts != 1 {
		t.Fatalf("puts after duplicate existing put = %d, want 1", state.Puts)
	}
}
func TestReadCacheCloseFlushesAsyncWrites(t *testing.T) {
	cacheDir := t.TempDir()
	key := strings.Repeat("a", sha256.Size*2)
	cache := newTestStore(t, cacheDir, 10<<20)
	cache.PutChunkAsync(key, int64(len("cached")), 0, []byte("cached"))
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	cache.PutChunkAsync(key, int64(len("ignored")), 1, []byte("ignored"))

	reopened := newTestStore(t, cacheDir, 10<<20)
	got, ok, err := reopened.GetChunk(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(got) != "cached" {
		t.Fatalf("closed cache chunk = %q ok=%v, want cached", got, ok)
	}
	if _, ok, err := reopened.GetChunk(key, 1); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("put after close was written")
	}
}
func TestReadCacheEvictionPrefersLargeChunksWhenLargePoolOverBudget(t *testing.T) {
	cacheDir := t.TempDir()
	smallKey := strings.Repeat("a", sha256.Size*2)
	largeKey := strings.Repeat("b", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	cache := newTestStore(t, cacheDir, 4*readChunkSize)

	if err := cache.PutChunk(smallKey, 1<<20, 0, chunk); err != nil {
		t.Fatal(err)
	}
	for i := range int64(5) {
		if err := cache.PutChunk(largeKey, 20<<20, i, chunk); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := cache.GetChunk(smallKey, 0); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("small-file chunk was evicted while large-file pool was over budget")
	}
	var largeChunks int
	for i := range int64(5) {
		if _, ok, err := cache.GetChunk(largeKey, i); err != nil {
			t.Fatal(err)
		} else if ok {
			largeChunks++
		}
	}
	if largeChunks >= 5 {
		t.Fatalf("large-file chunks were not evicted, still have %d", largeChunks)
	}
}
func TestReadCacheEvictionTreatsUnknownLargeCachedFileAsLarge(t *testing.T) {
	cacheDir := t.TempDir()
	smallKey := strings.Repeat("a", sha256.Size*2)
	legacyLargeKey := strings.Repeat("b", sha256.Size*2)
	chunk := bytes.Repeat([]byte("x"), readChunkSize)
	cache := newTestStore(t, cacheDir, 17*1024*1024)

	if err := cache.PutChunk(smallKey, 1<<20, 0, chunk); err != nil {
		t.Fatal(err)
	}
	largeChunkCount := int64(17*1024*1024/readChunkSize + 1)
	for i := range largeChunkCount {
		if err := cache.PutChunk(legacyLargeKey, 0, i, chunk); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok, err := cache.GetChunk(smallKey, 0); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("small-file chunk was evicted before unknown-size large cached file")
	}
	var largeChunks int
	for i := range largeChunkCount {
		if _, ok, err := cache.GetChunk(legacyLargeKey, i); err != nil {
			t.Fatal(err)
		} else if ok {
			largeChunks++
		}
	}
	if largeChunks >= int(largeChunkCount) {
		t.Fatalf("unknown-size large cached file was not treated as large, still have %d chunks", largeChunks)
	}
}
