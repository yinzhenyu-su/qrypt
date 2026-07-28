package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	cacheAccessWriteInterval = time.Second
)

func (c *readCacheStore) GetChunk(fid string, index int64) ([]byte, bool, error) {
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
	c.addHit()
	return data, true, nil
}
func (c *readCacheStore) GetChunkRange(fid string, index, start, size int64) ([]byte, bool, error) {
	data, _, ok, err := c.getChunkRange(fid, index, start, size, false)
	return data, ok, err
}
func (c *readCacheStore) GetChunkWithRange(fid string, index, start, size int64) ([]byte, []byte, bool, error) {
	return c.getChunkRange(fid, index, start, size, true)
}
func (c *readCacheStore) getChunkRange(fid string, index, start, size int64, includeChunk bool) ([]byte, []byte, bool, error) {
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
	if now := time.Now(); info.accessAt.Add(cacheAccessWriteInterval).Before(now) {
		info.accessAt = now
		fc.mu.Lock()
		fc.chunks[index] = info
		fc.mu.Unlock()
	}
	c.addHit()
	return data, chunk, true, nil
}
func (c *readCacheStore) HasChunk(fid string, index int64) (bool, error) {
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
func (c *readCacheStore) dropReadChunkIndex(fid string, index int64) {
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
func (c *readCacheStore) PutChunk(fid string, fileSize, index int64, data []byte) error {
	if err := c.putChunk(fid, fileSize, index, data); err != nil {
		return err
	}
	c.scheduleReadIndexSave()
	return nil
}
func (c *readCacheStore) putChunk(fid string, fileSize, index int64, data []byte) error {
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
func (c *readCacheStore) PutLocalFile(fid string, fileSize int64, localPath string) error {
	if fid == "" {
		return nil
	}
	if err := c.ensureReadCacheDir(); err != nil {
		c.setLastPutError(err)
		return err
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
func (c *readCacheStore) cleanupSeedFiles(files map[string]string) {
	for _, tmpPath := range files {
		_ = os.Remove(tmpPath)
	}
}
func (c *readCacheStore) replaceFileChunks(fid string, fileSize int64, chunks map[int64]chunkInfo) map[string]struct{} {
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
func (c *readCacheStore) InvalidateFile(fid string) {
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
func (c *readCacheStore) addHit() {
	c.stats.hits.Add(1)
}
func (c *readCacheStore) addMiss() {
	c.stats.misses.Add(1)
}
func (c *readCacheStore) addPut() {
	c.stats.puts.Add(1)
}
func (c *readCacheStore) addWriteDropped() {
	c.stats.writeDropped.Add(1)
}
func (c *readCacheStore) recordReadCacheWriteTiming(queueMS, writeMS int64) {
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
func (c *readCacheStore) setLastGetError(err error) {
	if err == nil {
		return
	}
	c.errMu.Lock()
	c.lastGetError = err.Error()
	c.lastGetAt = timeutil.Now()
	c.errMu.Unlock()
}
func (c *readCacheStore) setLastPutError(err error) {
	if err == nil {
		return
	}
	c.errMu.Lock()
	c.lastPutError = err.Error()
	c.lastPutAt = timeutil.Now()
	c.errMu.Unlock()
}
func (c *readCacheStore) fileChunks(fid string, fileSize ...int64) *fileChunks {
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
