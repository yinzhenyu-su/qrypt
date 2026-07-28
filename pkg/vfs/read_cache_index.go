package vfs

import (
	"encoding/json"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (c *readCacheStore) loadReadIndex() error {
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
func (c *readCacheStore) scheduleReadIndexSave() {
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
func (c *readCacheStore) FlushReadIndex() error {
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
func (c *readCacheStore) saveReadIndexNow() error {
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
func (c *readCacheStore) readIndexSnapshot() readCacheIndex {
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
func (c *readCacheStore) cleanupUnindexedReadCacheBatches(referenced map[string]struct{}) int {
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
func (c *readCacheStore) readIndexPath() string {
	return filepath.Join(c.dir, readCacheIndexName)
}
func (c *readCacheStore) readBatchPath(fid string, batch int64) string {
	return filepath.Join(c.dir, fmt.Sprintf("%s_%d.batch", cacheFileID(fid), batch))
}
func (c *readCacheStore) ensureReadCacheDir() error {
	return os.MkdirAll(c.dir, 0o755)
}
