package readcache

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCacheTruncatedBatchDropsOnlyStaleChunk(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"chunk", "range", "with-range"} {
		t.Run(method, func(t *testing.T) {
			cache := newTestStore(t, t.TempDir(), 10<<20)
			for i := range int64(2) {
				if err := cache.PutChunk("file", readChunkSize+6, i, []byte("cached"), testAccess); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Truncate(cache.readBatchPath("file", 0), readChunkSize+2); err != nil {
				t.Fatal(err)
			}
			var data []byte
			var hit bool
			var err error
			switch method {
			case "chunk":
				data, hit, err = cache.GetChunk("file", 1, testAccess)
			case "range":
				data, hit, err = cache.GetChunkRange("file", 1, 0, 6, testAccess)
			case "with-range":
				var full []byte
				data, full, hit, err = cache.GetChunkWithRange("file", 1, 0, 2, testAccess)
				if full != nil {
					t.Fatalf("corrupt full chunk returned: %q", full)
				}
			}
			if err != nil || hit || data != nil {
				t.Fatalf("truncated chunk = %q, hit=%t err=%v; want clean miss", data, hit, err)
			}
			if has, err := cache.HasChunk("file", 1); err != nil || has {
				t.Fatalf("stale index retained: has=%t err=%v", has, err)
			}
			if data, hit, err := cache.GetChunk("file", 0, testAccess); err != nil || !hit || string(data) != "cached" {
				t.Fatalf("healthy sibling = %q hit=%t err=%v", data, hit, err)
			}
			if got := cache.DebugSnapshot().Bytes; got != 6 {
				t.Fatalf("cached bytes = %d, want 6", got)
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := NewStore(cache.dir, 10<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reloaded.Close() })
			if has, err := reloaded.HasChunk("file", 1); err != nil || has {
				t.Fatalf("stale chunk resurrected after reload: has=%t err=%v", has, err)
			}
		})
	}
}

func TestReadCacheStartupRecoversInvalidIndex(t *testing.T) {
	t.Parallel()
	for _, contents := range []string{"{broken", `{"version":999}`, ""} {
		t.Run(contents, func(t *testing.T) {
			dir := t.TempDir()
			if contents != "" {
				if err := os.WriteFile(filepath.Join(dir, readCacheIndexName), []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"orphan.batch", "orphan.seed", "keep.txt"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cache, err := NewStore(dir, 10<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			for _, name := range []string{"orphan.batch", "orphan.seed", readCacheIndexName} {
				if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s survived invalid index recovery: %v", name, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(data) != "data" {
				t.Fatalf("unrelated file changed: %q err=%v", data, err)
			}
			if state := cache.DebugSnapshot(); state.Bytes != 0 || state.ChunkCount != 0 {
				t.Fatalf("recovered cache not empty: %+v", state)
			}
			if err := cache.PutChunk("fresh", 3, 0, []byte("new"), testAccess); err != nil {
				t.Fatalf("cache not writable after recovery: %v", err)
			}
		})
	}
}

func TestReadCacheStartupPrunesInvalidChunkRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	index := readCacheIndex{Version: readCacheIndexVersion, Files: map[string]readCacheIndexFile{
		"file": {Size: 6, Chunks: map[string]readCacheIndexChunk{
			"0":         {Batch: 0, Size: 6},
			"1":         {Batch: 1, Size: 6},            // missing batch
			"2":         {Batch: 0, Offset: 5, Size: 6}, // truncated range
			"3":         {Batch: 0, Size: 0},
			"bad-index": {Batch: 0, Size: 6},
		}},
	}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, readCacheIndexName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheFileID("file")+"_0.batch"), []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := NewStore(dir, 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	if state := cache.DebugSnapshot(); state.Bytes != 6 || state.ChunkCount != 1 {
		t.Fatalf("recovered index = %+v, want only healthy chunk", state)
	}
	if data, hit, err := cache.GetChunk("file", 0, testAccess); err != nil || !hit || string(data) != "cached" {
		t.Fatalf("healthy chunk = %q hit=%t err=%v", data, hit, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, readCacheIndexName))
	if err != nil {
		t.Fatal(err)
	}
	var saved readCacheIndex
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Files["file"].Chunks) != 1 || saved.Files["file"].Chunks["0"].Size != 6 {
		t.Fatalf("invalid records not removed from disk: %+v", saved)
	}
}

func TestReadCacheFailedSeedPreservesExistingData(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 10<<20)
	if err := cache.PutChunk("file", 3, 0, []byte("old"), testAccess); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("source interrupted")
	source := io.MultiReader(strings.NewReader("partial replacement"), failingCacheReader{wantErr})
	if err := cache.PutReader("file", 100, source); !errors.Is(err, wantErr) {
		t.Fatalf("PutReader error = %v, want source error", err)
	}
	if data, hit, err := cache.GetChunk("file", 0, testAccess); err != nil || !hit || string(data) != "old" {
		t.Fatalf("failed seed replaced valid data: %q hit=%t err=%v", data, hit, err)
	}
	if state := cache.DebugSnapshot(); state.Bytes != 3 || state.ChunkCount != 1 {
		t.Fatalf("failed seed changed accounting: %+v", state)
	}
	seeds, err := filepath.Glob(filepath.Join(cache.dir, "*.seed"))
	if err != nil || len(seeds) != 0 {
		t.Fatalf("seed files leaked: %v err=%v", seeds, err)
	}
}

type failingCacheReader struct{ err error }

func (r failingCacheReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadCacheAsyncWriteFailureDrainsAndRecovers(t *testing.T) {
	t.Parallel()
	cache := newTestStore(t, t.TempDir(), 10<<20)
	// A directory at the batch path fails writes on every OS, without relying
	// on permission bits (which do not fail for root).
	path := cache.readBatchPath("file", 0)
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cache.PutChunkAsync("file", 6, 0, []byte("cached"), testAccess)
	cache.WaitReadCacheWrites()
	if state := cache.DebugSnapshot(); state.Bytes != 0 || state.ChunkCount != 0 {
		t.Fatalf("failed write published a chunk: %+v", state)
	}
	cache.cacheWriteMu.Lock()
	inFlight := len(cache.cacheWritesInFlight)
	cache.cacheWriteMu.Unlock()
	if inFlight != 0 || cache.cacheWriteBytes.Load() != 0 {
		t.Fatalf("failed write not drained: in-flight=%d bytes=%d", inFlight, cache.cacheWriteBytes.Load())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cache.PutChunkAsync("file", 6, 0, []byte("cached"), testAccess)
	cache.WaitReadCacheWrites()
	if data, hit, err := cache.GetChunk("file", 0, testAccess); err != nil || !hit || string(data) != "cached" {
		t.Fatalf("retry after failed write = %q hit=%t err=%v", data, hit, err)
	}
}
