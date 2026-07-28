package vfs

import (
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"os"
	"sort"
	"time"
)

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
func (c *readCacheStore) evictIfNeeded() error {
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
	if fileSize >= readCacheLargeFileBytes {
		return true
	}
	return fileSize == 0 && cachedBytes >= readCacheLargeFileBytes
}
func (c *readCacheStore) removeReadChunk(fid string, index int64, expected chunkInfo) bool {
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
