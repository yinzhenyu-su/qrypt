package readcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cacheAccessWriteInterval = time.Second
	readChunkSize            = 1024 * 1024
)

func durationMillis(d time.Duration) int64 { return d.Milliseconds() }

func chunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

// enabled reports whether the read cache is active. A store constructed
// with max_size <= 0 is disabled: it never writes chunks, never persists an
// index, and reports every lookup as a miss.
func (c *Store) enabled() bool {
	return c.maxSize > 0
}

func (c *Store) GetChunk(fid string, index int64) ([]byte, bool, error) {
	if !c.enabled() {
		return nil, false, nil
	}
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
	if now := time.Now(); info.accessAt.Add(cacheAccessWriteInterval).Before(now) {
		info.accessAt = now
		fc.mu.Lock()
		fc.chunks[index] = info
		fc.mu.Unlock()
	}
	c.AddHit()
	return data, true, nil
}
func (c *Store) GetChunkRange(fid string, index, start, size int64) ([]byte, bool, error) {
	if !c.enabled() {
		return nil, false, nil
	}
	data, _, ok, err := c.getChunkRange(fid, index, start, size, false)
	return data, ok, err
}
func (c *Store) GetChunkWithRange(fid string, index, start, size int64) ([]byte, []byte, bool, error) {
	if !c.enabled() {
		return nil, nil, false, nil
	}
	return c.getChunkRange(fid, index, start, size, true)
}
func (c *Store) getChunkRange(fid string, index, start, size int64, includeChunk bool) ([]byte, []byte, bool, error) {
	if start < 0 || size < 0 {
		return nil, nil, false, fmt.Errorf("cache: chunk range must be non-negative")
	}
	sh := c.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
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
		c.AddHit()
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
	if now := time.Now(); info.accessAt.Add(cacheAccessWriteInterval).Before(now) {
		info.accessAt = now
		fc.mu.Lock()
		fc.chunks[index] = info
		fc.mu.Unlock()
	}
	c.AddHit()
	return data, chunk, true, nil
}
func (c *Store) HasChunk(fid string, index int64) (bool, error) {
	if !c.enabled() {
		return false, nil
	}
	sh := c.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
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
func (c *Store) dropReadChunkIndex(fid string, index int64) {
	sh := c.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
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
		sh := c.shardFor(fid)
		sh.mu.Lock()
		if sh.chunks[fid] == fc {
			delete(sh.chunks, fid)
		}
		sh.mu.Unlock()
	}
	c.scheduleReadIndexSave()
}
func isStaleReadCacheError(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
func (c *Store) PutChunk(fid string, fileSize, index int64, data []byte) error {
	if err := c.putChunk(fid, fileSize, index, data); err != nil {
		return err
	}
	c.scheduleReadIndexSave()
	return nil
}
func (c *Store) putChunk(fid string, fileSize, index int64, data []byte) error {
	if !c.enabled() {
		return nil
	}
	batch := index / cacheBatchBlocks
	offset := int64(index%cacheBatchBlocks) * readChunkSize
	path := c.readBatchPath(fid, batch)
	if err := c.ensureReadCacheDir(); err != nil {
		c.setLastPutError(err)
		return err
	}
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
func (c *Store) PutLocalFile(fid string, fileSize int64, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		c.setLastPutError(err)
		return err
	}
	defer f.Close()
	return c.PutReader(fid, fileSize, f)
}

func (c *Store) PutReader(fid string, fileSize int64, r io.Reader) error {
	if fid == "" {
		return nil
	}
	if !c.enabled() {
		return nil
	}
	if err := c.ensureReadCacheDir(); err != nil {
		c.setLastPutError(err)
		return err
	}

	now := time.Now()
	cacheID := cacheFileID(fid)
	newChunks := map[int64]chunkInfo{}
	tempFiles := map[string]string{}
	buf := make([]byte, readChunkSize)
	for index := int64(0); ; index++ {
		n, readErr := r.Read(buf)
		if n > 0 {
			batch := index / cacheBatchBlocks
			offset := int64(index%cacheBatchBlocks) * readChunkSize
			finalPath := filepath.Join(c.dir, fmt.Sprintf("%s_%d.batch", cacheID, batch))
			tmpPath := tempFiles[finalPath]
			if tmpPath == "" {
				tmpPath = filepath.Join(c.dir, fmt.Sprintf("%s_%d_%d.seed", cacheID, batch, now.UnixNano()))
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
			c.stats.evicted.Add(1)
		}
	}
	if err := c.evictIfNeeded(); err != nil {
		c.setLastPutError(err)
		return err
	}
	c.scheduleReadIndexSave()
	return nil
}
func (c *Store) cleanupSeedFiles(files map[string]string) {
	for _, tmpPath := range files {
		_ = os.Remove(tmpPath)
	}
}
func (c *Store) replaceFileChunks(fid string, fileSize int64, chunks map[int64]chunkInfo) map[string]struct{} {
	var newBytes int64
	for _, chunk := range chunks {
		newBytes += chunk.size
	}
	sh := c.shardFor(fid)
	sh.mu.Lock()
	old := sh.chunks[fid]
	sh.chunks[fid] = &fileChunks{fileSize: fileSize, chunks: chunks}
	sh.mu.Unlock()
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
func (c *Store) InvalidateFile(fid string) {
	sh := c.shardFor(fid)
	sh.mu.Lock()
	fc := sh.chunks[fid]
	delete(sh.chunks, fid)
	sh.mu.Unlock()
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
			c.stats.evicted.Add(1)
		}
	}
	c.scheduleReadIndexSave()
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

// Counters returns the hit/miss counters.
func (c *Store) Counters() (hits, misses int64) {
	return c.stats.hits.Load(), c.stats.misses.Load()
}

func (c *Store) AddHit() {
	c.stats.hits.Add(1)
}
func (c *Store) addMiss() {
	c.stats.misses.Add(1)
}
func (c *Store) addPut() {
	c.stats.puts.Add(1)
}
func (c *Store) addWriteDropped() {
	c.stats.writeDropped.Add(1)
}
func (c *Store) recordReadCacheWriteTiming(queueMS, writeMS int64) {
	c.stats.lastWriteQueueMS.Store(queueMS)
	for {
		cur := c.stats.maxWriteQueueMS.Load()
		if queueMS <= cur || c.stats.maxWriteQueueMS.CompareAndSwap(cur, queueMS) {
			break
		}
	}
	c.stats.lastWriteMS.Store(writeMS)
	for {
		cur := c.stats.maxWriteMS.Load()
		if writeMS <= cur || c.stats.maxWriteMS.CompareAndSwap(cur, writeMS) {
			break
		}
	}
}
func (c *Store) setLastGetError(err error) {
	if err == nil {
		return
	}
	c.errMu.Lock()
	c.lastGetError = err.Error()
	c.lastGetAt = util.Now()
	c.errMu.Unlock()
}
func (c *Store) setLastPutError(err error) {
	if err == nil {
		return
	}
	c.errMu.Lock()
	c.lastPutError = err.Error()
	c.lastPutAt = util.Now()
	c.errMu.Unlock()
}
func (c *Store) fileChunks(fid string, fileSize ...int64) *fileChunks {
	sh := c.shardFor(fid)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	fc := sh.chunks[fid]
	if fc == nil {
		fc = &fileChunks{chunks: map[int64]chunkInfo{}}
		sh.chunks[fid] = fc
	}
	if len(fileSize) > 0 && fileSize[0] > 0 {
		fc.fileSize = fileSize[0]
	}
	return fc
}

// --- read_cache_index.go ---

func (c *Store) loadReadIndex() error {
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
			sh := c.shardFor(fid)
			sh.mu.Lock()
			sh.chunks[fid] = fc
			sh.mu.Unlock()
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
func (c *Store) scheduleReadIndexSave() {
	c.readIndexMu.Lock()
	c.readIndexDirty = true
	c.readIndexMu.Unlock()
	c.debounce.Arm(readCacheIndexSaveDelay, func() {
		if err := c.FlushReadIndex(); err != nil {
			c.setLastPutError(err)
			logging.L.Warnf("[CACHE] save read cache index failed: %v", err)
		}
	})
}
func (c *Store) FlushReadIndex() error {
	c.readIndexSaveMu.Lock()
	defer c.readIndexSaveMu.Unlock()

	var lastErr error
	for {
		c.readIndexMu.Lock()
		c.debounce.Cancel()
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
func (c *Store) saveReadIndexNow() error {
	index := c.readIndexSnapshot()
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := c.ensureReadCacheDir(); err != nil {
		return err
	}
	return writeFileAtomic(c.readIndexPath(), data, 0o644)
}
func (c *Store) readIndexSnapshot() readCacheIndex {
	index := readCacheIndex{Version: readCacheIndexVersion}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for fid, fc := range sh.chunks {
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
		sh.mu.RUnlock()
	}
	return index
}
func (c *Store) cleanupUnindexedReadCacheBatches(referenced map[string]struct{}) int {
	entries, err := os.ReadDir(c.dir)
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
		if err := os.Remove(filepath.Join(c.dir, entry.Name())); err == nil {
			cleaned++
		}
	}
	return cleaned
}
func (c *Store) readIndexPath() string {
	return filepath.Join(c.dir, readCacheIndexName)
}
func (c *Store) readBatchPath(fid string, batch int64) string {
	return filepath.Join(c.dir, fmt.Sprintf("%s_%d.batch", cacheFileID(fid), batch))
}
func (c *Store) ensureReadCacheDir() error {
	return os.MkdirAll(c.dir, 0o755)
}

// --- read_cache_eviction.go ---

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
func (c *Store) evictIfNeeded() error {
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
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for fid, fc := range sh.chunks {
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
		sh.mu.RUnlock()
	}
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
	if fileSize >= ReadCacheLargeFileBytes {
		return true
	}
	return fileSize == 0 && cachedBytes >= ReadCacheLargeFileBytes
}
func (c *Store) removeReadChunk(fid string, index int64, expected chunkInfo) bool {
	sh := c.shardFor(fid)
	sh.mu.RLock()
	fc := sh.chunks[fid]
	sh.mu.RUnlock()
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
	c.stats.evicted.Add(1)
	return true
}

// --- read_cache_writer.go ---

func (c *Store) PutChunkAsync(fid string, fileSize, index int64, data []byte) {
	if !c.enabled() {
		return
	}
	if fid == "" || len(data) == 0 {
		return
	}
	if ok, err := c.HasChunk(fid, index); err != nil || ok {
		return
	}
	writeKey := chunkKey(fid, index)
	copied := make([]byte, len(data))
	copy(copied, data)
	c.cacheWriteWGMu.Lock()
	c.cacheWriteMu.Lock()
	if c.cacheWriteClosed {
		c.cacheWriteMu.Unlock()
		c.cacheWriteWGMu.Unlock()
		return
	}
	if _, exists := c.cacheWritesInFlight[writeKey]; exists {
		c.cacheWriteMu.Unlock()
		c.cacheWriteWGMu.Unlock()
		return
	}
	if len(c.cacheWriteQueue) >= cap(c.cacheWriteQueue) {
		c.cacheWriteMu.Unlock()
		c.cacheWriteWGMu.Unlock()
		c.addWriteDropped()
		logging.L.WarnfEvery("vfs.read_cache_queue_full", time.Second, "[CACHE] read cache write queue full; dropped chunk fid=%q index=%d size=%d", fid, index, len(data))
		return
	}
	c.cacheWritesInFlight[writeKey] = struct{}{}
	c.cacheWriteWG.Add(1)
	write := readCacheWrite{fid: fid, fileSize: fileSize, index: index, data: copied, queuedAt: util.Now()}
	c.cacheWriteBytes.Add(int64(len(copied)))
	select {
	case c.cacheWriteQueue <- write:
	default:
		c.cacheWriteBytes.Add(-int64(len(copied)))
		delete(c.cacheWritesInFlight, writeKey)
		c.cacheWriteWG.Done()
		c.addWriteDropped()
		logging.L.WarnfEvery("vfs.read_cache_queue_full", time.Second, "[CACHE] read cache write queue full; dropped chunk fid=%q index=%d size=%d", fid, index, len(data))
	}
	c.cacheWriteMu.Unlock()
	c.cacheWriteWGMu.Unlock()
}
func (c *Store) runReadCacheWriter() {
	defer c.cacheWriterWG.Done()
	for write := range c.cacheWriteQueue {
		writes := []readCacheWrite{write}
	drain:
		for len(writes) < readCacheWriteBatchLimit {
			select {
			case next, ok := <-c.cacheWriteQueue:
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
func (c *Store) handleReadCacheWrites(writes []readCacheWrite) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.L.Warnf("[CACHE] async put chunk panic recovered writes=%d panic=%v", len(writes), recovered)
		}
		for range writes {
			c.cacheWriteWG.Done()
		}
		c.cacheWriteMu.Lock()
		for _, write := range writes {
			c.cacheWriteBytes.Add(-int64(len(write.data)))
			delete(c.cacheWritesInFlight, chunkKey(write.fid, write.index))
		}
		c.cacheWriteMu.Unlock()
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
	if err := c.ensureReadCacheDir(); err != nil {
		c.setLastPutError(err)
		for _, write := range writes {
			logging.L.Warnf("[CACHE] async put chunk failed fid=%q index=%d size=%d err=%v", write.fid, write.index, len(write.data), err)
		}
		return
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
			writeStarted := util.Now()
			if err := c.writeReadCacheChunk(f, path, write); err != nil {
				c.setLastPutError(err)
				logging.L.Warnf("[CACHE] async put chunk failed fid=%q index=%d size=%d err=%v", write.fid, write.index, len(write.data), err)
				continue
			}
			c.recordReadCacheWriteTiming(durationMillis(writeStarted.Sub(write.queuedAt)), durationMillis(util.Now().Sub(writeStarted)))
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
func (c *Store) writeReadCacheChunk(f *os.File, path string, write readCacheWrite) error {
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
func (c *Store) WaitReadCacheWrites() {
	c.cacheWriteWGMu.Lock()
	c.cacheWriteWG.Wait()
	c.cacheWriteWGMu.Unlock()
}
func (c *Store) FlushReadCache() error {
	if !c.enabled() {
		return nil
	}
	c.cacheWriteWGMu.Lock()
	defer c.cacheWriteWGMu.Unlock()
	c.cacheWriteWG.Wait()
	return c.FlushReadIndex()
}
func (c *Store) ClearReadCache() error {
	if !c.enabled() {
		return nil
	}
	c.cacheWriteWGMu.Lock()
	defer c.cacheWriteWGMu.Unlock()
	c.cacheWriteWG.Wait()

	c.readIndexMu.Lock()
	c.debounce.Cancel()
	c.readIndexDirty = false
	c.readIndexMu.Unlock()

	var removed int64
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		for _, fc := range sh.chunks {
			fc.mu.Lock()
			removed += int64(len(fc.chunks))
			fc.chunks = map[int64]chunkInfo{}
			fc.mu.Unlock()
		}
		sh.chunks = map[string]*fileChunks{}
		sh.mu.Unlock()
	}
	c.readBytes.Store(0)
	c.stats.evicted.Add(removed)

	readingDir := c.dir
	if err := os.RemoveAll(readingDir); err != nil {
		c.setLastPutError(err)
		return err
	}
	if err := os.MkdirAll(readingDir, 0o755); err != nil {
		c.setLastPutError(err)
		return err
	}
	return nil
}
func (c *Store) Close() error {
	if !c.enabled() {
		return nil
	}
	c.cacheWriteMu.Lock()
	if !c.cacheWriteClosed {
		c.cacheWriteClosed = true
		close(c.cacheWriteQueue)
	}
	c.cacheWriteMu.Unlock()
	c.cacheWriterWG.Wait()
	return c.FlushReadCache()
}
