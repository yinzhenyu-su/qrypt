package vfs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
)

const cacheBatchBlocks = 16

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
	LastAttemptAt int64                 `json:"last_attempt_at,omitempty"`
	NextAttemptAt int64                 `json:"next_attempt_at,omitempty"`
	Staging       *PendingStagingStatus `json:"staging,omitempty"`
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
	mu     sync.RWMutex
	chunks map[int64]chunkInfo
}

type Cache struct {
	dir     string
	maxSize int64
	staging *stagingStore

	mu            sync.RWMutex
	journalMu     sync.Mutex
	pending       map[string]PendingFile
	chunks        map[string]*fileChunks
	lastDiskCheck atomic.Int64 // unix nano
	stats         cacheStats
	lastGetError  string
	lastGetAt     time.Time
	lastPutError  string
	lastPutAt     time.Time
}

type cacheStats struct {
	hits    int64
	misses  int64
	puts    int64
	evicted int64
}

func NewCache(dir string, maxSize int64) (*Cache, error) {
	readingDir := filepath.Join(dir, "reading")
	if err := os.MkdirAll(readingDir, 0o755); err != nil {
		return nil, err
	}
	// Clean up orphaned batch files from previous runs.
	if entries, err := os.ReadDir(readingDir); err == nil {
		var cleaned int
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".batch") {
				_ = os.Remove(filepath.Join(readingDir, entry.Name()))
				cleaned++
			}
		}
		if cleaned > 0 {
			logging.L.Infof("[CACHE] cleaned %d orphaned batch files", cleaned)
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
	c := &Cache{
		dir:     dir,
		maxSize: adjusted,
		staging: staging,
		pending: map[string]PendingFile{},
		chunks:  map[string]*fileChunks{},
	}
	if err := c.loadJournal(); err != nil {
		return nil, err
	}
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

func (c *Cache) RemovePending(path string) error {
	c.mu.Lock()
	pending, ok := c.pending[path]
	delete(c.pending, path)
	c.mu.Unlock()
	if ok {
		_ = c.staging.remove(pending.LocalPath)
	}
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
		return nil, false, err
	}
	defer f.Close()
	data := make([]byte, info.size)
	if _, err := f.ReadAt(data, info.offset); err != nil {
		c.addMiss()
		c.setLastGetError(err)
		return nil, false, err
	}
	info.accessAt = time.Now()
	fc.mu.Lock()
	fc.chunks[index] = info
	fc.mu.Unlock()
	c.addHit()
	return data, true, nil
}

func (c *Cache) PutChunk(fid string, index int64, data []byte) error {
	batch := index / cacheBatchBlocks
	offset := int64(index%cacheBatchBlocks) * readChunkSize
	path := filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d.batch", cacheFileID(fid), batch))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
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
	fc := c.fileChunks(fid)
	fc.mu.Lock()
	fc.chunks[index] = chunkInfo{file: path, offset: offset, size: int64(len(data)), accessAt: time.Now()}
	fc.mu.Unlock()
	c.addPut()
	if err := c.evictIfNeeded(); err != nil {
		c.setLastPutError(err)
		return err
	}
	return nil
}

func (c *Cache) PutLocalFile(fid, localPath string) error {
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
	generation := now.UnixNano()
	newChunks := map[int64]chunkInfo{}
	tempFiles := map[string]string{}
	buf := make([]byte, readChunkSize)
	for index := int64(0); ; index++ {
		n, readErr := f.Read(buf)
		if n > 0 {
			batch := index / cacheBatchBlocks
			offset := int64(index%cacheBatchBlocks) * readChunkSize
			finalPath := filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d_%d.batch", cacheID, batch, generation))
			tmpPath := tempFiles[finalPath]
			if tmpPath == "" {
				tmpPath = filepath.Join(c.dir, "reading", fmt.Sprintf("%s_%d_%d.seed", cacheID, batch, generation))
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
	oldFiles := c.replaceFileChunks(fid, newChunks)
	for range tempFiles {
		c.addPut()
	}
	for file := range oldFiles {
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
	return nil
}

func (c *Cache) cleanupSeedFiles(files map[string]string) {
	for _, tmpPath := range files {
		_ = os.Remove(tmpPath)
	}
}

func (c *Cache) replaceFileChunks(fid string, chunks map[int64]chunkInfo) map[string]struct{} {
	c.mu.Lock()
	old := c.chunks[fid]
	c.chunks[fid] = &fileChunks{chunks: chunks}
	c.mu.Unlock()
	files := map[string]struct{}{}
	if old == nil {
		return files
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	for _, chunk := range old.chunks {
		files[chunk.file] = struct{}{}
	}
	old.chunks = map[int64]chunkInfo{}
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
	fc.mu.Lock()
	for _, chunk := range fc.chunks {
		files[chunk.file] = struct{}{}
	}
	fc.chunks = map[int64]chunkInfo{}
	fc.mu.Unlock()

	for file := range files {
		if err := os.Remove(file); err == nil {
			c.mu.Lock()
			c.stats.evicted++
			c.mu.Unlock()
		}
	}
}

func cacheFileID(fid string) string {
	sum := sha256.Sum256([]byte(fid))
	return hex.EncodeToString(sum[:])
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

func (c *Cache) fileChunks(fid string) *fileChunks {
	c.mu.Lock()
	defer c.mu.Unlock()
	fc := c.chunks[fid]
	if fc == nil {
		fc = &fileChunks{chunks: map[int64]chunkInfo{}}
		c.chunks[fid] = fc
	}
	return fc
}

func (c *Cache) journalPath() string {
	return filepath.Join(c.dir, "pending.jsonl")
}

func (c *Cache) appendJournal(entry journalEntry) error {
	c.journalMu.Lock()
	defer c.journalMu.Unlock()
	return c.appendJournalLocked(entry)
}

func (c *Cache) appendJournalLocked(entry journalEntry) error {
	f, err := os.OpenFile(c.journalPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (c *Cache) loadJournal() error {
	f, err := os.Open(c.journalPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
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
	return scanner.Err()
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
		a.LastAttemptAt == b.LastAttemptAt &&
		a.NextAttemptAt == b.NextAttemptAt
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

	var total int64
	var chunks []struct {
		fid string
		idx int64
		ch  chunkInfo
	}
	c.mu.RLock()
	for fid, fc := range c.chunks {
		fc.mu.RLock()
		for idx, ch := range fc.chunks {
			total += ch.size
			chunks = append(chunks, struct {
				fid string
				idx int64
				ch  chunkInfo
			}{fid: fid, idx: idx, ch: ch})
		}
		fc.mu.RUnlock()
	}
	c.mu.RUnlock()
	if total <= maxSize {
		return nil
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].ch.accessAt.Before(chunks[j].ch.accessAt) })
	var evicted int
	for _, item := range chunks {
		if total <= maxSize*7/10 {
			break
		}
		fc := c.fileChunks(item.fid)
		fc.mu.Lock()
		// Only remove the batch file if no other chunk still references it.
		stillReferenced := false
		for idx, ch := range fc.chunks {
			if ch.file == item.ch.file && idx != item.idx {
				stillReferenced = true
				break
			}
		}
		if !stillReferenced {
			_ = os.Remove(item.ch.file)
		}
		delete(fc.chunks, item.idx)
		fc.mu.Unlock()
		c.mu.Lock()
		c.stats.evicted++
		c.mu.Unlock()
		total -= item.ch.size
		evicted++
	}
	logging.L.Infof("[CACHE] evicted %d chunks size=%d max_size=%d", evicted, total, maxSize)
	return nil
}
