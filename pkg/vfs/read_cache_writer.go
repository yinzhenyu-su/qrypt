package vfs

import (
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"os"
	"time"
)

func (c *readCacheStore) PutChunkAsync(fid string, fileSize, index int64, data []byte) {
	if !c.enabled() {
		return
	}
	if fid == "" || len(data) == 0 {
		return
	}
	if ok, err := c.HasChunk(fid, index); err != nil || ok {
		return
	}
	writeKey := readChunkKey(fid, index)
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
	write := readCacheWrite{fid: fid, fileSize: fileSize, index: index, data: copied, queuedAt: timeutil.Now()}
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
func (c *readCacheStore) runReadCacheWriter() {
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
func (c *readCacheStore) handleReadCacheWrites(writes []readCacheWrite) {
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
			delete(c.cacheWritesInFlight, readChunkKey(write.fid, write.index))
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
			writeStarted := timeutil.Now()
			if err := c.writeReadCacheChunk(f, path, write); err != nil {
				c.setLastPutError(err)
				logging.L.Warnf("[CACHE] async put chunk failed fid=%q index=%d size=%d err=%v", write.fid, write.index, len(write.data), err)
				continue
			}
			c.recordReadCacheWriteTiming(durationMillis(writeStarted.Sub(write.queuedAt)), durationMillis(timeutil.Now().Sub(writeStarted)))
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
func (c *readCacheStore) writeReadCacheChunk(f *os.File, path string, write readCacheWrite) error {
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
func (c *readCacheStore) WaitReadCacheWrites() {
	c.cacheWriteWGMu.Lock()
	c.cacheWriteWG.Wait()
	c.cacheWriteWGMu.Unlock()
}
func (c *readCacheStore) FlushReadCache() error {
	if !c.enabled() {
		return nil
	}
	c.cacheWriteWGMu.Lock()
	defer c.cacheWriteWGMu.Unlock()
	c.cacheWriteWG.Wait()
	return c.FlushReadIndex()
}
func (c *readCacheStore) ClearReadCache() error {
	if !c.enabled() {
		return nil
	}
	c.cacheWriteWGMu.Lock()
	defer c.cacheWriteWGMu.Unlock()
	c.cacheWriteWG.Wait()

	c.readIndexMu.Lock()
	if c.readIndexTimer != nil {
		c.readIndexTimer.Stop()
		c.readIndexTimer = nil
	}
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
func (c *readCacheStore) Close() error {
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
