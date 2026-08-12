package readcache

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const (
	cacheBatchBlocks         = 16
	readCacheIndexVersion    = 1
	readCacheIndexName       = "index.json"
	ReadCacheLargeFileBytes  = 16 << 20
	readCacheSmallReserveDiv = 4
	readCacheWriteQueueSize  = 64
	readCacheWriteBatchLimit = 16
	readCacheIndexSaveDelay  = 30 * time.Second
)

type chunkInfo struct {
	file     string
	offset   int64
	size     int64
	accessAt time.Time
}

type fileChunks struct {
	fileSize int64
	mu       sync.RWMutex
	chunks   map[int64]chunkInfo
}

type readCacheIndex struct {
	Version int                           `json:"version"`
	Files   map[string]readCacheIndexFile `json:"files,omitempty"`
}
type readCacheIndexFile struct {
	Size   int64                          `json:"size,omitempty"`
	Chunks map[string]readCacheIndexChunk `json:"chunks,omitempty"`
}
type readCacheIndexChunk struct {
	Batch    int64     `json:"batch"`
	Offset   int64     `json:"offset"`
	Size     int64     `json:"size"`
	AccessAt time.Time `json:"access_at"`
}
type readCacheWrite struct {
	fid      string
	fileSize int64
	index    int64
	data     []byte
	queuedAt time.Time
}

// Store is the durable read-cache store: chunk batches on disk plus an
// in-memory index with sharded locking. A store built with max_size <= 0
// is disabled and reports every lookup as a miss.
type Store struct {
	dir     string
	maxSize int64

	shards        [readCacheShards]shard
	readBytes     atomic.Int64
	lastDiskCheck atomic.Int64 // unix nano
	stats         cacheStats
	errMu         sync.Mutex
	lastGetError  string
	lastGetAt     time.Time
	lastPutError  string
	lastPutAt     time.Time

	cacheWriteQueue     chan readCacheWrite
	cacheWriteBytes     atomic.Int64
	cacheWriteWG        sync.WaitGroup
	cacheWriteWGMu      sync.Mutex
	cacheWriterWG       sync.WaitGroup
	cacheWriteMu        sync.Mutex
	cacheWritesInFlight map[string]struct{}
	cacheWriteClosed    bool
	readIndexSaveMu     sync.Mutex
	readIndexMu         sync.Mutex
	readIndexDirty      bool
	debounce            Debouncer
}

// readCacheShards is the number of independent lock domains over the
// in-memory chunk index. Sharding by file id keeps concurrent reads of
// different files from contending on a single global lock, and isolates a
// window-load write (one shard) from reads of other files (other shards).
const readCacheShards = 16

type shard struct {
	mu     sync.RWMutex
	chunks map[string]*fileChunks
}

func (c *Store) shardFor(fid string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fid))
	return &c.shards[h.Sum32()%readCacheShards]
}

type cacheStats struct {
	hits             atomic.Int64
	misses           atomic.Int64
	puts             atomic.Int64
	evicted          atomic.Int64
	writeDropped     atomic.Int64
	lastWriteMS      atomic.Int64
	maxWriteMS       atomic.Int64
	lastWriteQueueMS atomic.Int64
	maxWriteQueueMS  atomic.Int64
}

// NewStore opens (or creates) the read cache directory. maxSize <= 0
// disables the cache: no writer goroutine, no write queue, no index load,
// and every public method short-circuits. The directory is still created
// and orphaned seed files are cleaned so a previously-enabled mount leaves
// no debris behind.
func NewStore(dir string, maxSize int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Clean up incomplete cache seed files from previous runs. Completed batch
	// files are reconciled after loading the persistent read-cache index.
	if entries, err := os.ReadDir(dir); err == nil {
		var cleaned int
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), ".seed") || entry.Name() == readCacheIndexName+".tmp" {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
				cleaned++
			}
		}
		if cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d orphaned read cache seed files", cleaned)
		}
	}
	if maxSize <= 0 {
		return &Store{dir: dir, debounce: newTimeDebouncer()}, nil
	}
	adjusted, reason := limitByDiskSpace(maxSize, dir)
	if reason != "" {
		logging.L.Infof("[CACHE] %s", reason)
	}
	store := &Store{
		dir:                 dir,
		maxSize:             adjusted,
		cacheWriteQueue:     make(chan readCacheWrite, readCacheWriteQueueSize),
		cacheWritesInFlight: map[string]struct{}{},
		cacheWriteClosed:    false,
		debounce:            newTimeDebouncer(),
	}
	for i := range store.shards {
		store.shards[i].chunks = map[string]*fileChunks{}
	}
	if err := store.loadReadIndex(); err != nil {
		logging.L.Warnf("[CACHE] load read cache index failed: %v", err)
	}
	store.cacheWriterWG.Add(1)
	go store.runReadCacheWriter()
	return store, nil
}

// diskFreeBytes reports free bytes on the filesystem containing path.
func diskFreeBytes(path string) (int64, error) {
	_, free, err := util.DiskFree(path)
	return free, err
}

// writeFileAtomic writes data to path via a temp file + rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
