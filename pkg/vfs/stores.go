package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/logging"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	cacheBatchBlocks         = 16
	readCacheIndexVersion    = 1
	readCacheIndexName       = "index.json"
	readCacheLargeFileBytes  = 16 << 20
	readCacheSmallReserveDiv = 4
	readCacheWriteQueueSize  = 64
	readCacheWriteBatchLimit = 16
	readCacheIndexSaveDelay  = 30 * time.Second
	journalCompactMaxBytes   = 512 << 10
	journalCompactMaxEntries = 1024
)

// reserveFraction and minReserveBytes control how much disk space is kept
// free when capping cache maxSize by available disk space.
type PendingUpload struct {
	Path          string               `json:"path"`
	FID           string               `json:"fid"`
	ParentID      string               `json:"parent_id"`
	Name          string               `json:"name"`
	LocalPath     string               `json:"local_path"`
	Size          int64                `json:"size"`
	ModTime       int64                `json:"mod_time,omitempty"`
	UpdatedAt     int64                `json:"updated_at"`
	RetryCount    int                  `json:"retry_count,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	PermanentFail bool                 `json:"permanent_fail,omitempty"`
	LastAttemptAt int64                `json:"last_attempt_at,omitempty"`
	NextAttemptAt int64                `json:"next_attempt_at,omitempty"`
	ReplaceUpload *UploadReplacement   `json:"replace_upload,omitempty"`
	Staging       *UploadStagingStatus `json:"staging,omitempty"`
	Frozen        bool                 `json:"frozen,omitempty"`
}
type UploadReplacement struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
}
type UploadStagingStatus struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size,omitempty"`
	SizeMatches bool   `json:"size_matches"`
	Error       string `json:"error,omitempty"`
}
type journalEntry struct {
	Op string `json:"op"`
	PendingUpload
}
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
type Stores struct {
	*readCacheStore
	*uploadStore
}
type uploadStore struct {
	dir     string
	staging *stagingStore

	mu        sync.RWMutex
	journalMu sync.Mutex
	pending   map[string]PendingUpload
}
type readCacheStore struct {
	dir     string
	maxSize int64

	shards        [readCacheShards]readCacheShard
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
	readIndexTimer      *time.Timer
}

// readCacheShards is the number of independent lock domains over the
// in-memory chunk index. Sharding by file id keeps concurrent reads of
// different files from contending on a single global lock, and isolates a
// window-load write (one shard) from reads of other files (other shards).
const readCacheShards = 16

type readCacheShard struct {
	mu     sync.RWMutex
	chunks map[string]*fileChunks
}

func (c *readCacheStore) shardFor(fid string) *readCacheShard {
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

func NewStoresInDir(dir string, maxSize int64) (*Stores, error) {
	return NewStores(dir, filepath.Join(dir, "reading"), maxSize)
}
func NewStores(uploadDir, readCacheDir string, maxSize int64) (*Stores, error) {
	readCache, err := newReadCacheStore(readCacheDir, maxSize)
	if err != nil {
		return nil, err
	}
	uploads, err := newUploadStore(uploadDir)
	if err != nil {
		_ = readCache.Close()
		return nil, err
	}
	return &Stores{readCacheStore: readCache, uploadStore: uploads}, nil
}
func newUploadStore(dir string) (*uploadStore, error) {
	staging, err := newStagingStore(filepath.Join(dir, "staging"))
	if err != nil {
		return nil, err
	}
	if cleaned := staging.cleanupUploadTemps(); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d orphaned staging upload files", cleaned)
	}
	store := &uploadStore{
		dir:     dir,
		staging: staging,
		pending: map[string]PendingUpload{},
	}
	entries, err := store.loadJournal()
	if err != nil {
		return nil, err
	}
	if store.shouldCompactJournal(entries) {
		if err := store.compactJournal(); err != nil {
			logging.L.Warnf("[CACHE] compact pending journal failed: %v", err)
		}
	}
	if cleaned := store.sweepUnreferencedStaging(); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d unreferenced staging files", cleaned)
	}
	return store, nil
}
func newReadCacheStore(dir string, maxSize int64) (*readCacheStore, error) {
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
	// maxSize <= 0 disables the read cache: no writer goroutine, no write
	// queue, no index load, and every public method short-circuits. The
	// directory is still created and orphaned seed files are cleaned so a
	// previously-enabled mount leaves no debris behind.
	if maxSize <= 0 {
		return &readCacheStore{dir: dir}, nil
	}
	adjusted, reason := limitByDiskSpace(maxSize, dir)
	if reason != "" {
		logging.L.Infof("[CACHE] %s", reason)
	}
	store := &readCacheStore{
		dir:                 dir,
		maxSize:             adjusted,
		cacheWriteQueue:     make(chan readCacheWrite, readCacheWriteQueueSize),
		cacheWritesInFlight: map[string]struct{}{},
		cacheWriteClosed:    false,
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
