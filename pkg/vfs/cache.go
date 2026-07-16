package vfs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
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
const (
	diskReserveFraction = 0.1              // reserve at least 10% of available space
	diskMinReserveBytes = 1 << 30          // at least 1GB
	diskCheckInterval   = 10 * time.Second // how often to re-check disk in evict loop
)

// limitByDiskSpace caps maxSize so that at least diskReserveFraction (and at
// least diskMinReserveBytes) of the filesystem remains free.  Returns the
// adjusted size and a human-readable reason if an adjustment was made.
func limitByDiskSpace(maxSize int64, dir string) (int64, string) {
	if maxSize <= 0 {
		return 0, ""
	}
	avail, err := diskFreeBytes(dir)
	if err != nil {
		return maxSize, ""
	}
	reserve := int64(float64(avail) * diskReserveFraction)
	if reserve < diskMinReserveBytes {
		reserve = diskMinReserveBytes
	}
	ceiling := avail - reserve
	if ceiling <= 0 {
		// Disk space is already below the reserve threshold.  Keep
		// eviction working by setting a small floor instead of 0.
		floor := int64(64 << 20)
		if avail/4 < floor {
			floor = avail / 4
		}
		if floor < 1 {
			floor = 1
		}
		return floor, fmt.Sprintf(
			"disk too full: available=%d reserve=%d floor=%d", avail, reserve, floor)
	}
	if maxSize > ceiling {
		return ceiling, fmt.Sprintf(
			"max_size=%d capped by disk: available=%d reserve=%d effective=%d",
			maxSize, avail, reserve, ceiling)
	}
	return maxSize, ""
}

type PendingFile struct {
	Path          string                `json:"path"`
	FID           string                `json:"fid"`
	ParentID      string                `json:"parent_id"`
	Name          string                `json:"name"`
	LocalPath     string                `json:"local_path"`
	Size          int64                 `json:"size"`
	ModTime       int64                 `json:"mod_time,omitempty"`
	UpdatedAt     int64                 `json:"updated_at"`
	RetryCount    int                   `json:"retry_count,omitempty"`
	LastError     string                `json:"last_error,omitempty"`
	PermanentFail bool                  `json:"permanent_fail,omitempty"`
	LastAttemptAt int64                 `json:"last_attempt_at,omitempty"`
	NextAttemptAt int64                 `json:"next_attempt_at,omitempty"`
	ReplaceUpload *PendingReplaceUpload `json:"replace_upload,omitempty"`
	Staging       *PendingStagingStatus `json:"staging,omitempty"`
}

type PendingReplaceUpload struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
}

type PendingStagingStatus struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size,omitempty"`
	SizeMatches bool   `json:"size_matches"`
	Error       string `json:"error,omitempty"`
}

type journalEntry struct {
	Op string `json:"op"`
	PendingFile
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
}

type Cache struct {
	dir     string
	maxSize int64
	staging *stagingStore

	mu            sync.RWMutex
	journalMu     sync.Mutex
	pending       map[string]PendingFile
	chunks        map[string]*fileChunks
	readBytes     atomic.Int64
	lastDiskCheck atomic.Int64 // unix nano
	stats         cacheStats
	lastGetError  string
	lastGetAt     time.Time
	lastPutError  string
	lastPutAt     time.Time

	readWriteQueue  chan readCacheWrite
	readWriteWG     sync.WaitGroup
	readWriteWGMu   sync.Mutex
	readWriterWG    sync.WaitGroup
	readWriteMu     sync.Mutex
	readWrites      map[string]struct{}
	readWriteClosed bool
	readIndexSaveMu sync.Mutex
	readIndexMu     sync.Mutex
	readIndexDirty  bool
	readIndexTimer  *time.Timer
}

type cacheStats struct {
	hits         int64
	misses       int64
	puts         int64
	evicted      int64
	writeDropped int64
}

func NewCache(dir string, maxSize int64) (*Cache, error) {
	readingDir := filepath.Join(dir, "reading")
	if err := os.MkdirAll(readingDir, 0o755); err != nil {
		return nil, err
	}
	// Clean up incomplete cache seed files from previous runs. Completed batch
	// files are reconciled after loading the persistent read-cache index.
	if entries, err := os.ReadDir(readingDir); err == nil {
		var cleaned int
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), ".seed") || entry.Name() == readCacheIndexName+".tmp" {
				_ = os.Remove(filepath.Join(readingDir, entry.Name()))
				cleaned++
			}
		}
		if cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d orphaned read cache seed files", cleaned)
		}
	}
	adjusted, reason := limitByDiskSpace(maxSize, dir)
	if reason != "" {
		logging.L.Infof("[CACHE] %s", reason)
	}
	staging, err := newStagingStore(filepath.Join(dir, "staging"))
	if err != nil {
		return nil, err
	}
	if cleaned := staging.cleanupUploadTemps(); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d orphaned staging upload files", cleaned)
	}
	c := &Cache{
		dir:            dir,
		maxSize:        adjusted,
		staging:        staging,
		pending:        map[string]PendingFile{},
		chunks:         map[string]*fileChunks{},
		readWriteQueue: make(chan readCacheWrite, readCacheWriteQueueSize),
		readWrites:     map[string]struct{}{},
	}
	if err := c.loadReadIndex(); err != nil {
		logging.L.Warnf("[CACHE] load read cache index failed: %v", err)
	}
	entries, err := c.loadJournal()
	if err != nil {
		return nil, err
	}
	if c.shouldCompactJournal(entries) {
		if err := c.compactJournal(); err != nil {
			logging.L.Warnf("[CACHE] compact pending journal failed: %v", err)
		}
	}
	c.readWriterWG.Add(1)
	go c.runReadCacheWriter()
	return c, nil
}

func (c *Cache) Pending() []PendingFile {
	c.mu.RLock()
	files := make([]PendingFile, 0, len(c.pending))
	for _, pending := range c.pending {
		files = append(files, pending)
	}
	c.mu.RUnlock()
	for i := range files {
		files[i].Staging = c.pendingStagingStatus(files[i])
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func (c *Cache) pendingStagingStatus(p PendingFile) *PendingStagingStatus {
	status := &PendingStagingStatus{}
	info, err := os.Stat(p.LocalPath)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Exists = true
	status.Size = info.Size()
	status.SizeMatches = status.Size == p.Size
	return status
}

func (c *Cache) PendingByPath(path string) (PendingFile, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pending, ok := c.pending[path]
	return pending, ok
}

func (c *Cache) SavePending(p PendingFile) error {
	p.UpdatedAt = timeutil.Now().UnixNano()
	c.mu.Lock()
	c.pending[p.Path] = p
	c.mu.Unlock()
	return c.appendJournal(journalEntry{Op: "dirty", PendingFile: p})
}

func (c *Cache) UpdatePendingTransient(p PendingFile) {
	p.UpdatedAt = timeutil.Now().UnixNano()
	c.mu.Lock()
	c.pending[p.Path] = p
	c.mu.Unlock()
}

func (c *Cache) RecordPendingFailure(path string, err error, retryDelay time.Duration) (PendingFile, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[path]
	if ok {
		pending.RetryCount++
		if err != nil {
			pending.LastError = err.Error()
		}
		pending.LastAttemptAt = now.UnixNano()
		if retryDelay > 0 {
			pending.NextAttemptAt = now.Add(retryDelay).UnixNano()
		} else {
			pending.NextAttemptAt = 0
		}
		pending.UpdatedAt = now.UnixNano()
		c.pending[path] = pending
	}
	c.mu.Unlock()
	if !ok {
		return PendingFile{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingFile: pending})
}

func (c *Cache) RecordPendingReplaceUploadIfUnchanged(p PendingFile, upload PendingReplaceUpload) (PendingFile, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[p.Path]
	if ok && samePendingFile(pending, p) {
		pending.ReplaceUpload = &upload
		pending.LastError = ""
		pending.NextAttemptAt = 0
		pending.UpdatedAt = now.UnixNano()
		c.pending[p.Path] = pending
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return PendingFile{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingFile: pending})
}

func (c *Cache) RecordPendingPermanentFailure(path string, err error) (PendingFile, bool, error) {
	now := timeutil.Now()
	c.mu.Lock()
	pending, ok := c.pending[path]
	if ok {
		pending.RetryCount++
		if err != nil {
			pending.LastError = err.Error()
		}
		pending.PermanentFail = true
		pending.LastAttemptAt = now.UnixNano()
		pending.NextAttemptAt = 0
		pending.UpdatedAt = now.UnixNano()
		c.pending[path] = pending
	}
	c.mu.Unlock()
	if !ok {
		return PendingFile{}, false, nil
	}
	return pending, true, c.appendJournal(journalEntry{Op: "dirty", PendingFile: pending})
}

func (c *Cache) RemovePending(path string) error {
	c.mu.Lock()
	pending, ok := c.pending[path]
	delete(c.pending, path)
	c.mu.Unlock()
	if !ok {
		return nil
	}
	_ = c.staging.remove(pending.LocalPath)
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingFile: PendingFile{Path: path}}); err != nil {
		return err
	}
	return c.compactJournalLocked()
}

func (c *Cache) RemovePendingUnder(dir string) error {
	dir = cleanVirtual(dir)
	c.mu.Lock()
	var removed []PendingFile
	for path, pending := range c.pending {
		if path == dir || isPathUnder(path, dir) {
			delete(c.pending, path)
			removed = append(removed, pending)
		}
	}
	c.mu.Unlock()
	if len(removed) == 0 {
		return nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	for _, pending := range removed {
		_ = c.staging.remove(pending.LocalPath)
		if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingFile: PendingFile{Path: pending.Path}}); err != nil {
			return err
		}
	}
	return c.compactJournalLocked()
}

func (c *Cache) RemovePendingIfUnchanged(p PendingFile) (bool, error) {
	c.mu.Lock()
	current, ok := c.pending[p.Path]
	if ok && samePendingFile(current, p) {
		delete(c.pending, p.Path)
	} else {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingFile: PendingFile{Path: p.Path}}); err != nil {
		return false, err
	}
	return true, c.compactJournalLocked()
}

func (c *Cache) RenamePending(oldPath string, next PendingFile) error {
	c.mu.Lock()
	delete(c.pending, oldPath)
	c.pending[next.Path] = next
	c.mu.Unlock()
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(journalEntry{Op: "clean", PendingFile: PendingFile{Path: oldPath}}); err != nil {
		return err
	}
	if err := c.appendJournalLocked(journalEntry{Op: "dirty", PendingFile: next}); err != nil {
		return err
	}
	return c.compactJournalLocked()
}

func (c *Cache) GetChunk(fid string, index int64) ([]byte, bool, error) {
	fc := c.fileChunks(fid)
	fc.mu.RLock()
	info, ok := fc.chunks[index]
	fc.mu.RUnlock()
	if !ok {
		c.addMiss()
		return nil, false, nil
	}
	f, err := os.Open(info.file)
	if err != nil {
		c.addMiss()
		c.setLastGetError(err)
		if isStaleReadCacheError(err) {
			c.dropReadChunkIndex(fid, index)
			return nil, false, nil
		}
		return nil, false, err
	}
	defer f.Close()
	data := make([]byte, info.size)
	if _, err := f.ReadAt(data, info.offset); err != nil {
		c.addMiss()
		c.setLastGetError(err)
		if isStaleReadCacheError(err) {
			c.dropReadChunkIndex(fid, index)
			return nil, false, nil
		}
		return nil, false, err
	}
	info.accessAt = time.Now()
	fc.mu.Lock()
	fc.chunks[index] = info
	fc.mu.Unlock()
	c.addHit()
	return data, true, nil
}

func (c *Cache) GetChunkRange(fid string, index, start, size int64) ([]byte, bool, error) {
	data, _, ok, err := c.getChunkRange(fid, index, start, size, false)
	return data, ok, err
}

func (c *Cache) GetChunkWithRange(fid string, index, start, size int64) ([]byte, []byte, bool, error) {
	return c.getChunkRange(fid, index, start, size, true)
}

func (c *Cache) getChunkRange(fid string, index, start, size int64, includeChunk bool) ([]byte, []byte, bool, error) {
	if start < 0 || size < 0 {
		return nil, nil, false, fmt.Errorf("cache: chunk range must be non-negative")
	}
	c.mu.RLock()
	fc := c.chunks[fid]
	c.mu.RUnlock()
	if fc == nil {
		c.addMiss()
		return nil, nil, false, nil
	}
	fc.mu.RLock()
	info, ok := fc.chunks[index]
	fc.mu.RUnlock()
	if !ok {
		c.addMiss()
		return nil, nil, false, nil
	}
	if start >= info.size {
		c.addHit()
		return nil, nil, true, nil
	}
	if size == 0 || start+size > info.size {
		size = info.size - start
	}
	f, err := os.Open(info.file)
	if err != nil {
		c.addMiss()
		c.setLastGetError(err)
		if isStaleReadCacheError(err) {
			c.dropReadChunkIndex(fid, index)
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	defer f.Close()
	var full []byte
	if includeChunk {
		full = make([]byte, info.size)
		if _, err := f.ReadAt(full, info.offset); err != nil {
			c.addMiss()
			c.setLastGetError(err)
			if isStaleReadCacheError(err) {
				c.dropReadChunkIndex(fid, index)
				return nil, nil, false, nil
			}
			return nil, nil, false, err
		}
	} else {
		full = make([]byte, size)
		if _, err := f.ReadAt(full, info.offset+start); err != nil {
			c.addMiss()
			c.setLastGetError(err)
			if isStaleReadCacheError(err) {
				c.dropReadChunkIndex(fid, index)
				return nil, nil, false, nil
			}
			return nil, nil, false, err
		}
	}
	data := full
	chunk := full
	if includeChunk {
		data = full[start : start+size]
	} else {
		chunk = nil
	}
	if len(data) != int(size) {
		c.addMiss()
		err := io.ErrUnexpectedEOF
		c.setLastGetError(err)
		c.dropReadChunkIndex(fid, index)
		return nil, nil, false, nil
	}
	info.accessAt = time.Now()
	fc.mu.Lock()
	fc.chunks[index] = info
	fc.mu.Unlock()
	c.addHit()
	return data, chunk, true, nil
}

func (c *Cache) HasChunk(fid string, index int64) (bool, error) {
	c.mu.RLock()
	fc := c.chunks[fid]
	c.mu.RUnlock()
	if fc == nil {
		return false, nil
	}
	fc.mu.RLock()
	_, ok := fc.chunks[index]
	fc.mu.RUnlock()
	if !ok {
		return false, nil
	}
	return true, nil
}

func (c *Cache) dropReadChunkIndex(fid string, index int64) {
	c.mu.RLock()
	fc := c.chunks[fid]
	c.mu.RUnlock()
	if fc == nil {
		return
	}
	fc.mu.Lock()
	info, ok := fc.chunks[index]
	if ok {
		delete(fc.chunks, index)
		c.readBytes.Add(-info.size)
	}
	empty := len(fc.chunks) == 0
	fc.mu.Unlock()
	if empty {
		c.mu.Lock()
		if c.chunks[fid] == fc {
			delete(c.chunks, fid)
		}
		c.mu.Unlock()
	}
	c.scheduleReadIndexSave()
}

func isStaleReadCacheError(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (c *Cache) PutChunk(fid string, fileSize, index int64, data []byte) error {
	if err := c.putChunk(fid, fileSize, index, data); err != nil {
		return err
	}
	c.scheduleReadIndexSave()
	return nil
}

func (c *Cache) PutChunkAsync(fid string, fileSize, index int64, data []byte) {
	if fid == "" || len(data) == 0 {
		return
	}
	if ok, err := c.HasChunk(fid, index); err != nil || ok {
		return
	}
	writeKey := readChunkKey(fid, index)
	copied := make([]byte, len(data))
	copy(copied, data)
	c.readWriteWGMu.Lock()
	c.readWriteMu.Lock()
	if c.readWriteClosed {
		c.readWriteMu.Unlock()
		c.readWriteWGMu.Unlock()
		return
	}
	if _, exists := c.readWrites[writeKey]; exists {
		c.readWriteMu.Unlock()
		c.readWriteWGMu.Unlock()
		return
	}
	if len(c.readWriteQueue) >= cap(c.readWriteQueue) {
		c.readWriteMu.Unlock()
		c.readWriteWGMu.Unlock()
		c.addWriteDropped()
		logging.L.WarnfEvery("vfs.read_cache_queue_full", time.Second, "[CACHE] read cache write queue full; dropped chunk fid=%q index=%d size=%d", fid, index, len(data))
		return
	}
	c.readWrites[writeKey] = struct{}{}
	c.readWriteWG.Add(1)
	write := readCacheWrite{fid: fid, fileSize: fileSize, index: index, data: copied}
	select {
	case c.readWriteQueue <- write:
	default:
		delete(c.readWrites, writeKey)
		c.readWriteWG.Done()
		c.addWriteDropped()
		logging.L.WarnfEvery("vfs.read_cache_queue_full", time.Second, "[CACHE] read cache write queue full; dropped chunk fid=%q index=%d size=%d", fid, index, len(data))
	}
	c.readWriteMu.Unlock()
	c.readWriteWGMu.Unlock()
}

func (c *Cache) runReadCacheWriter() {
	defer c.readWriterWG.Done()
	for write := range c.readWriteQueue {
		writes := []readCacheWrite{write}
	drain:
		for len(writes) < readCacheWriteBatchLimit {
			select {
			case next, ok := <-c.readWriteQueue:
				if !ok {
					break drain
				}
				writes = append(writes, next)
			default:
				break drain
			}
		}
		c.handleReadCacheWrites(writes)
	}
}

func (c *Cache) handleReadCacheWrites(writes []readCacheWrite) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.L.Warnf("[CACHE] async put chunk panic recovered writes=%d panic=%v", len(writes), recovered)
		}
		for range writes {
			c.readWriteWG.Done()
		}
		c.readWriteMu.Lock()
		for _, write := range writes {
			delete(c.readWrites, readChunkKey(write.fid, write.index))
		}
		c.readWriteMu.Unlock()
	}()
	groups := map[string][]readCacheWrite{}
	var paths []string
	for _, write := range writes {
		path := c.readBatchPath(write.fid, write.index/cacheBatchBlocks)
		if _, ok := groups[path]; !ok {
			paths = append(paths, path)
		}
		groups[path] = append(groups[path], write)
	}
	wrote := false
	for _, path := range paths {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			c.setLastPutError(err)
			for _, write := range groups[path] {
				logging.L.Warnf("[CACHE] async put chunk failed fid=%q index=%d size=%d err=%v", write.fid, write.index, len(write.data), err)
			}
			continue
		}
		for _, write := range groups[path] {
			if err := c.writeReadCacheChunk(f, path, write); err != nil {
				c.setLastPutError(err)
				logging.L.Warnf("[CACHE] async put chunk failed fid=%q index=%d size=%d err=%v", write.fid, write.index, len(write.data), err)
				continue
			}
			wrote = true
		}
		if err := f.Close(); err != nil {
			c.setLastPutError(err)
			logging.L.Warnf("[CACHE] async put batch close failed path=%q err=%v", path, err)
		}
	}
	if wrote {
		if err := c.evictIfNeeded(); err != nil {
			c.setLastPutError(err)
		}
		c.scheduleReadIndexSave()
	}
}

func (c *Cache) writeReadCacheChunk(f *os.File, path string, write readCacheWrite) error {
	offset := int64(write.index%cacheBatchBlocks) * readChunkSize
	if _, err := f.WriteAt(write.data, offset); err != nil {
		return err
	}
	fc := c.fileChunks(write.fid, write.fileSize)
	fc.mu.Lock()
	if write.fileSize > 0 {
		fc.fileSize = write.fileSize
	}
	old, existed := fc.chunks[write.index]
	fc.chunks[write.index] = chunkInfo{file: path, offset: offset, size: int64(len(write.data)), accessAt: time.Now()}
	fc.mu.Unlock()
	delta := int64(len(write.data))
	if existed {
		delta -= old.size
	}
	c.readBytes.Add(delta)
	c.addPut()
	return nil
}

func (c *Cache) WaitReadCacheWrites() {
	c.readWriteWGMu.Lock()
	c.readWriteWG.Wait()
	c.readWriteWGMu.Unlock()
}

func (c *Cache) FlushReadCache() error {
	c.readWriteWGMu.Lock()
	defer c.readWriteWGMu.Unlock()
	c.readWriteWG.Wait()
	return c.FlushReadIndex()
}

func (c *Cache) Close() error {
	c.readWriteMu.Lock()
	if !c.readWriteClosed {
		c.readWriteClosed = true
		close(c.readWriteQueue)
	}
	c.readWriteMu.Unlock()
	c.readWriterWG.Wait()
	return c.FlushReadCache()
}

func (c *Cache) putChunk(fid string, fileSize, index int64, data []byte) error {
	batch := index / cacheBatchBlocks
	offset := int64(index%cacheBatchBlocks) * readChunkSize
	path := c.readBatchPath(fid, batch)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		c.setLastPutError(err)
		return err
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		f.Close()
		c.setLastPutError(err)
		return err
	}
	if err := f.Close(); err != nil {
		c.setLastPutError(err)
		return err
	}
	fc := c.fileChunks(fid, fileSize)
	fc.mu.Lock()
	if fileSize > 0 {
		fc.fileSize = fileSize
	}
	old, existed := fc.chunks[index]
	fc.chunks[index] = chunkInfo{file: path, offset: offset, size: int64(len(data)), accessAt: time.Now()}
	fc.mu.Unlock()
	delta := int64(len(data))
	if existed {
		delta -= old.size
	}
	c.readBytes.Add(delta)
	c.addPut()
	if err := c.evictIfNeeded(); err != nil {
		c.setLastPutError(err)
		return err
	}
	return nil
}

func (c *Cache) PutLocalFile(fid string, fileSize int64, localPath string) error {
	if fid == "" {
		return nil
	}
	f, err := os.Open(localPath)
	if err != nil {
		c.setLastPutError(err)
		return err
	}
	defer f.Close()

	now := time.Now()
	cacheID := cacheFileID(fid)
	newChunks := map[int64]chunkInfo{}
	tempFiles := map[string]string{}
	buf := make([]byte, readChunkSize)
	for index := int64(0); ; index++ {
		n, readErr := f.Read(buf)
		if n > 0 {
			batch := index / cacheBatchBlocks
			offset := int64(index%cacheBatchBlocks) * readChunkSize
			finalPath := filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d.batch", cacheID, batch))
			tmpPath := tempFiles[finalPath]
			if tmpPath == "" {
				tmpPath = filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d_%d.seed", cacheID, batch, now.UnixNano()))
				tempFiles[finalPath] = tmpPath
			}
			out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				c.cleanupSeedFiles(tempFiles)
				c.setLastPutError(err)
				return err
			}
			if _, err := out.WriteAt(buf[:n], offset); err != nil {
				out.Close()
				c.cleanupSeedFiles(tempFiles)
				c.setLastPutError(err)
				return err
			}
			if err := out.Close(); err != nil {
				c.cleanupSeedFiles(tempFiles)
				c.setLastPutError(err)
				return err
			}
			newChunks[index] = chunkInfo{file: finalPath, offset: offset, size: int64(n), accessAt: now}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			c.cleanupSeedFiles(tempFiles)
			c.setLastPutError(readErr)
			return readErr
		}
	}

	renamedFiles := map[string]struct{}{}
	for finalPath, tmpPath := range tempFiles {
		if err := os.Rename(tmpPath, finalPath); err != nil {
			c.cleanupSeedFiles(tempFiles)
			for file := range renamedFiles {
				_ = os.Remove(file)
			}
			c.setLastPutError(err)
			return err
		}
		renamedFiles[finalPath] = struct{}{}
	}
	oldFiles := c.replaceFileChunks(fid, fileSize, newChunks)
	for range tempFiles {
		c.addPut()
	}
	for file := range oldFiles {
		if _, replaced := renamedFiles[file]; replaced {
			continue
		}
		if err := os.Remove(file); err == nil {
			c.mu.Lock()
			c.stats.evicted++
			c.mu.Unlock()
		}
	}
	if err := c.evictIfNeeded(); err != nil {
		c.setLastPutError(err)
		return err
	}
	c.scheduleReadIndexSave()
	return nil
}

func (c *Cache) cleanupSeedFiles(files map[string]string) {
	for _, tmpPath := range files {
		_ = os.Remove(tmpPath)
	}
}

func (c *Cache) replaceFileChunks(fid string, fileSize int64, chunks map[int64]chunkInfo) map[string]struct{} {
	var newBytes int64
	for _, chunk := range chunks {
		newBytes += chunk.size
	}
	c.mu.Lock()
	old := c.chunks[fid]
	c.chunks[fid] = &fileChunks{fileSize: fileSize, chunks: chunks}
	c.mu.Unlock()
	files := map[string]struct{}{}
	if old == nil {
		c.readBytes.Add(newBytes)
		return files
	}
	var oldBytes int64
	old.mu.Lock()
	defer old.mu.Unlock()
	for _, chunk := range old.chunks {
		oldBytes += chunk.size
		files[chunk.file] = struct{}{}
	}
	old.chunks = map[int64]chunkInfo{}
	c.readBytes.Add(newBytes - oldBytes)
	return files
}

func (c *Cache) InvalidateFile(fid string) {
	c.mu.Lock()
	fc := c.chunks[fid]
	delete(c.chunks, fid)
	c.mu.Unlock()
	if fc == nil {
		return
	}

	files := map[string]struct{}{}
	var removedBytes int64
	fc.mu.Lock()
	for _, chunk := range fc.chunks {
		removedBytes += chunk.size
		files[chunk.file] = struct{}{}
	}
	fc.chunks = map[int64]chunkInfo{}
	fc.mu.Unlock()
	c.readBytes.Add(-removedBytes)

	for file := range files {
		if err := os.Remove(file); err == nil {
			c.mu.Lock()
			c.stats.evicted++
			c.mu.Unlock()
		}
	}
	c.scheduleReadIndexSave()
}

func (c *Cache) loadReadIndex() error {
	path := c.readIndexPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if cleaned := c.cleanupUnindexedReadCacheBatches(nil); cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d unindexed read cache batch files", cleaned)
		}
		return nil
	}
	if err != nil {
		return err
	}
	var index readCacheIndex
	if err := json.Unmarshal(data, &index); err != nil {
		if cleaned := c.cleanupUnindexedReadCacheBatches(nil); cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d read cache batch files after invalid index", cleaned)
		}
		_ = os.Remove(path)
		return err
	}
	if index.Version != readCacheIndexVersion {
		if cleaned := c.cleanupUnindexedReadCacheBatches(nil); cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d read cache batch files after unsupported index version", cleaned)
		}
		_ = os.Remove(path)
		return nil
	}

	referenced := map[string]struct{}{}
	changed := false
	for fid, file := range index.Files {
		if fid == "" || len(file.Chunks) == 0 {
			continue
		}
		fc := &fileChunks{fileSize: file.Size, chunks: map[int64]chunkInfo{}}
		for indexText, chunk := range file.Chunks {
			chunkIndex, err := strconv.ParseInt(indexText, 10, 64)
			if err != nil || chunk.Size <= 0 {
				changed = true
				continue
			}
			batchPath := c.readBatchPath(fid, chunk.Batch)
			info, err := os.Stat(batchPath)
			if err != nil || info.Size() < chunk.Offset+chunk.Size {
				changed = true
				continue
			}
			fc.chunks[chunkIndex] = chunkInfo{
				file:     batchPath,
				offset:   chunk.Offset,
				size:     chunk.Size,
				accessAt: chunk.AccessAt,
			}
			c.readBytes.Add(chunk.Size)
			referenced[filepath.Base(batchPath)] = struct{}{}
		}
		if len(fc.chunks) > 0 {
			c.chunks[fid] = fc
		}
	}
	if cleaned := c.cleanupUnindexedReadCacheBatches(referenced); cleaned > 0 {
		logging.L.Infof("[CACHE] cleaned %d unindexed read cache batch files", cleaned)
	}
	if changed {
		return c.saveReadIndexNow()
	}
	return nil
}

func (c *Cache) scheduleReadIndexSave() {
	c.readIndexMu.Lock()
	c.readIndexDirty = true
	if c.readIndexTimer != nil {
		c.readIndexMu.Unlock()
		return
	}
	c.readIndexTimer = time.AfterFunc(readCacheIndexSaveDelay, func() {
		if err := c.FlushReadIndex(); err != nil {
			c.setLastPutError(err)
			logging.L.Warnf("[CACHE] save read cache index failed: %v", err)
		}
	})
	c.readIndexMu.Unlock()
}

func (c *Cache) FlushReadIndex() error {
	c.readIndexSaveMu.Lock()
	defer c.readIndexSaveMu.Unlock()

	var lastErr error
	for {
		c.readIndexMu.Lock()
		if c.readIndexTimer != nil {
			c.readIndexTimer.Stop()
			c.readIndexTimer = nil
		}
		if !c.readIndexDirty {
			c.readIndexMu.Unlock()
			return lastErr
		}
		c.readIndexDirty = false
		c.readIndexMu.Unlock()

		if err := c.saveReadIndexNow(); err != nil {
			lastErr = err
			c.setLastPutError(err)
		}
	}
}

func (c *Cache) saveReadIndexNow() error {
	index := c.readIndexSnapshot()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(c.readIndexPath(), data, 0o644)
}

func (c *Cache) readIndexSnapshot() readCacheIndex {
	index := readCacheIndex{Version: readCacheIndexVersion}
	c.mu.RLock()
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		for chunkIndex, info := range fc.chunks {
			if info.size <= 0 {
				continue
			}
			if index.Files == nil {
				index.Files = map[string]readCacheIndexFile{}
			}
			file := index.Files[fid]
			file.Size = fc.fileSize
			if file.Chunks == nil {
				file.Chunks = map[string]readCacheIndexChunk{}
			}
			file.Chunks[strconv.FormatInt(chunkIndex, 10)] = readCacheIndexChunk{
				Batch:    chunkIndex / cacheBatchBlocks,
				Offset:   info.offset,
				Size:     info.size,
				AccessAt: info.accessAt,
			}
			index.Files[fid] = file
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	return index
}

func (c *Cache) cleanupUnindexedReadCacheBatches(referenced map[string]struct{}) int {
	entries, err := os.ReadDir(filepath.Join(c.dir, "reading"))
	if err != nil {
		return 0
	}
	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".batch") {
			continue
		}
		if referenced != nil {
			if _, ok := referenced[entry.Name()]; ok {
				continue
			}
		}
		if err := os.Remove(filepath.Join(c.dir, "reading", entry.Name())); err == nil {
			cleaned++
		}
	}
	return cleaned
}

func (c *Cache) readIndexPath() string {
	return filepath.Join(c.dir, "reading", readCacheIndexName)
}

func (c *Cache) readBatchPath(fid string, batch int64) string {
	return filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d.batch", cacheFileID(fid), batch))
}

func cacheFileID(fid string) string {
	if isSHA256Hex(fid) {
		return fid
	}
	sum := sha256.Sum256([]byte(fid))
	return hex.EncodeToString(sum[:])
}

func isSHA256Hex(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func (c *Cache) addHit() {
	c.mu.Lock()
	c.stats.hits++
	c.mu.Unlock()
}

func (c *Cache) addMiss() {
	c.mu.Lock()
	c.stats.misses++
	c.mu.Unlock()
}

func (c *Cache) addPut() {
	c.mu.Lock()
	c.stats.puts++
	c.mu.Unlock()
}

func (c *Cache) addWriteDropped() {
	c.mu.Lock()
	c.stats.writeDropped++
	c.mu.Unlock()
}

func (c *Cache) setLastGetError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastGetError = err.Error()
	c.lastGetAt = timeutil.Now()
	c.mu.Unlock()
}

func (c *Cache) setLastPutError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.lastPutError = err.Error()
	c.lastPutAt = timeutil.Now()
	c.mu.Unlock()
}

func (c *Cache) fileChunks(fid string, fileSize ...int64) *fileChunks {
	c.mu.Lock()
	defer c.mu.Unlock()
	fc := c.chunks[fid]
	if fc == nil {
		fc = &fileChunks{chunks: map[int64]chunkInfo{}}
		c.chunks[fid] = fc
	}
	if len(fileSize) > 0 && fileSize[0] > 0 {
		fc.fileSize = fileSize[0]
	}
	return fc
}

func (c *Cache) journalPath() string {
	return filepath.Join(c.dir, "pending.jsonl")
}

func (c *Cache) appendJournal(entry journalEntry) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	if err := c.appendJournalLocked(entry); err != nil {
		return err
	}
	if c.shouldCompactJournal(0) {
		return c.compactJournalLocked()
	}
	return nil
}

func (c *Cache) appendJournalLocked(entry journalEntry) error {
	f, err := os.OpenFile(c.journalPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}

func (c *Cache) loadJournal() (int, error) {
	f, err := os.Open(c.journalPath())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var entries int
	for scanner.Scan() {
		entries++
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry.Op {
		case "dirty":
			if _, err := os.Stat(entry.LocalPath); err == nil {
				c.pending[entry.Path] = entry.PendingFile
			}
		case "clean":
			delete(c.pending, entry.Path)
		}
	}
	return entries, scanner.Err()
}

func (c *Cache) shouldCompactJournal(entries int) bool {
	info, err := os.Stat(c.journalPath())
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return false
	}
	if info.Size() >= journalCompactMaxBytes {
		return true
	}
	if entries == 0 {
		entries = countJournalEntries(c.journalPath())
	}
	c.mu.RLock()
	pendingCount := len(c.pending)
	c.mu.RUnlock()
	return entries >= journalCompactMaxEntries && entries > pendingCount+32
}

func countJournalEntries(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var entries int
	for scanner.Scan() {
		entries++
	}
	if err := scanner.Err(); err != nil {
		logging.L.Warnf("[CACHE] count pending journal entries failed: %v", err)
	}
	return entries
}

func (c *Cache) compactJournal() error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	return c.compactJournalLocked()
}

func (c *Cache) compactJournalLocked() error {
	tmp := c.journalPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c.mu.RLock()
	for _, p := range c.pending {
		data, err := json.Marshal(journalEntry{Op: "dirty", PendingFile: p})
		if err != nil {
			c.mu.RUnlock()
			f.Close()
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			c.mu.RUnlock()
			f.Close()
			return err
		}
	}
	c.mu.RUnlock()
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.journalPath()); err != nil {
		return err
	}
	_ = syncParentDir(c.journalPath())
	return nil
}

func writeFileSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

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

func syncParentDir(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func samePendingFile(a, b PendingFile) bool {
	return a.Path == b.Path &&
		a.FID == b.FID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.LocalPath == b.LocalPath &&
		a.Size == b.Size &&
		a.ModTime == b.ModTime &&
		a.UpdatedAt == b.UpdatedAt &&
		a.RetryCount == b.RetryCount &&
		a.LastError == b.LastError &&
		a.PermanentFail == b.PermanentFail &&
		a.LastAttemptAt == b.LastAttemptAt &&
		a.NextAttemptAt == b.NextAttemptAt &&
		samePendingReplaceUpload(a.ReplaceUpload, b.ReplaceUpload)
}

func samePendingReplaceUpload(a, b *PendingReplaceUpload) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ID == b.ID &&
		a.ParentID == b.ParentID &&
		a.Name == b.Name &&
		a.Size == b.Size
}

func (c *Cache) evictIfNeeded() error {
	maxSize := c.maxSize
	if maxSize <= 0 {
		return nil
	}

	// Periodically re-check disk space.  If the filesystem is getting full
	// from other processes, tighten the cap so cache doesn't cause disk-full.
	lastCheck := time.Unix(0, c.lastDiskCheck.Load())
	if time.Since(lastCheck) >= diskCheckInterval {
		c.lastDiskCheck.Store(time.Now().UnixNano())
		if adjusted, _ := limitByDiskSpace(maxSize, c.dir); adjusted < maxSize {
			maxSize = adjusted
		}
	}
	if c.readBytes.Load() <= maxSize {
		return nil
	}

	var total int64
	var largeTotal int64
	var chunks []struct {
		fid   string
		idx   int64
		ch    chunkInfo
		large bool
	}
	c.mu.RLock()
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		var fileBytes int64
		for _, ch := range fc.chunks {
			fileBytes += ch.size
		}
		large := readCacheFileLarge(fc.fileSize, fileBytes)
		for idx, ch := range fc.chunks {
			total += ch.size
			if large {
				largeTotal += ch.size
			}
			chunks = append(chunks, struct {
				fid   string
				idx   int64
				ch    chunkInfo
				large bool
			}{fid: fid, idx: idx, ch: ch, large: large})
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	if total <= maxSize {
		return nil
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ch.accessAt.Before(chunks[j].ch.accessAt) })
	var evicted int
	targetSize := maxSize * 7 / 10
	largeBudget := maxSize - maxSize/readCacheSmallReserveDiv
	for _, item := range chunks {
		if largeTotal <= largeBudget && total <= maxSize {
			break
		}
		if !item.large {
			continue
		}
		if c.removeReadChunk(item.fid, item.idx, item.ch) {
			total -= item.ch.size
			largeTotal -= item.ch.size
			evicted++
		}
	}
	if total > maxSize {
		for _, item := range chunks {
			if total <= targetSize {
				break
			}
			if c.removeReadChunk(item.fid, item.idx, item.ch) {
				total -= item.ch.size
				evicted++
			}
		}
	}
	logging.L.Infof("[CACHE] evicted %d chunks size=%d max_size=%d", evicted, total, maxSize)
	if evicted > 0 {
		c.scheduleReadIndexSave()
	}
	return nil
}

func readCacheFileLarge(fileSize, cachedBytes int64) bool {
	if fileSize >= readCacheLargeFileBytes {
		return true
	}
	return fileSize == 0 && cachedBytes >= readCacheLargeFileBytes
}

func (c *Cache) removeReadChunk(fid string, index int64, expected chunkInfo) bool {
	c.mu.RLock()
	fc := c.chunks[fid]
	c.mu.RUnlock()
	if fc == nil {
		return false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	current, ok := fc.chunks[index]
	if !ok || current.file != expected.file || current.offset != expected.offset {
		return false
	}
	stillReferenced := false
	for idx, ch := range fc.chunks {
		if ch.file == current.file && idx != index {
			stillReferenced = true
			break
		}
	}
	if !stillReferenced {
		_ = os.Remove(current.file)
	}
	delete(fc.chunks, index)
	c.readBytes.Add(-current.size)
	c.mu.Lock()
	c.stats.evicted++
	c.mu.Unlock()
	return true
}
