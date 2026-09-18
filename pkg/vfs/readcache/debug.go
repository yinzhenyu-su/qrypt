package readcache

import (
	"sort"
	"time"
)

// DebugReadCache is a point-in-time snapshot of the read cache for
// debugging and tests.
type DebugReadCache struct {
	Enabled            bool  `json:"enabled"`
	MaxBytes           int64 `json:"max_bytes"`
	LargeFileThreshold int64 `json:"large_file_threshold"`
	ChunkCount         int   `json:"chunk_count"`
	Bytes              int64 `json:"bytes"`
	LargeFileBytes     int64 `json:"large_file_bytes"`
	SmallFileBytes     int64 `json:"small_file_bytes"`
	// UnprovenBytes is content no read run has come back for; a sequential
	// stream reading back its own read-ahead lands here. ProvenBytes is the
	// rest and equals ProbationaryBytes + ProtectedBytes.
	UnprovenBytes int64 `json:"unproven_bytes"`
	ProvenBytes   int64 `json:"proven_bytes"`
	// StreamBytes is the part of that unproven content the sequential
	// sub-budget is charging: unproven bytes in the large-file class. An
	// eviction pass cuts it back to a share of the large-class ceiling.
	StreamBytes int64 `json:"stream_bytes"`
	// ProtectedBytes and ProbationaryBytes split the proven content by segment;
	// the drain order empties probationary before touching protected.
	ProtectedBytes      int64                `json:"protected_bytes"`
	ProbationaryBytes   int64                `json:"probationary_bytes"`
	FileCount           int                  `json:"file_count"`
	Hits                int64                `json:"hits"`
	Misses              int64                `json:"misses"`
	Puts                int64                `json:"puts"`
	Evicted             int64                `json:"evicted"`
	EvictedBytes        int64                `json:"evicted_bytes"`
	Promotions          int64                `json:"promotions"`
	LastGetError        string               `json:"last_get_error,omitempty"`
	LastGetErrorAt      *time.Time           `json:"last_get_error_at,omitempty"`
	LastPutError        string               `json:"last_put_error,omitempty"`
	LastPutErrorAt      *time.Time           `json:"last_put_error_at,omitempty"`
	WriteQueueLength    int                  `json:"write_queue_len"`
	WriteQueueCap       int                  `json:"write_queue_cap"`
	WriteQueueBytes     int64                `json:"write_queue_bytes"`
	WriteQueueMaxBytes  int64                `json:"write_queue_max_bytes"`
	WriteQueueDropped   int64                `json:"write_queue_dropped"`
	LastWriteMS         int64                `json:"last_write_ms,omitempty"`
	MaxWriteMS          int64                `json:"max_write_ms,omitempty"`
	LastWriteQueueMS    int64                `json:"last_write_queue_ms,omitempty"`
	MaxWriteQueueMS     int64                `json:"max_write_queue_ms,omitempty"`
	IndexDirty          bool                 `json:"index_dirty"`
	IndexFlushScheduled bool                 `json:"index_flush_scheduled"`
	Files               []DebugReadCacheFile `json:"files,omitempty"`
}

// DebugReadCacheFile describes one cached file in a DebugReadCache.
type DebugReadCacheFile struct {
	ID         string `json:"id"`
	Size       int64  `json:"size,omitempty"`
	Large      bool   `json:"large,omitempty"`
	ChunkCount int    `json:"chunk_count"`
	Bytes      int64  `json:"bytes"`
}

// DebugSnapshot returns a point-in-time view of the store for debugging.
func (c *Store) DebugSnapshot() DebugReadCache {
	snapshot := DebugReadCache{Enabled: c.enabled(), MaxBytes: c.maxSize, LargeFileThreshold: ReadCacheLargeFileBytes}
	if !c.enabled() {
		return snapshot
	}
	snapshot.WriteQueueLength = len(c.cacheWriteQueue)
	snapshot.WriteQueueCap = cap(c.cacheWriteQueue)
	snapshot.WriteQueueBytes = c.cacheWriteBytes.Load()
	snapshot.WriteQueueMaxBytes = int64(cap(c.cacheWriteQueue)) * readChunkSize
	c.readIndexMu.Lock()
	snapshot.IndexDirty = c.readIndexDirty
	snapshot.IndexFlushScheduled = c.debounce.Armed()
	c.readIndexMu.Unlock()
	fileCount := 0
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		fileCount += len(sh.chunks)
		sh.mu.RUnlock()
	}
	snapshot.FileCount = fileCount
	snapshot.Hits = c.stats.hits.Load()
	snapshot.Misses = c.stats.misses.Load()
	snapshot.Puts = c.stats.puts.Load()
	snapshot.Evicted = c.stats.evicted.Load()
	snapshot.EvictedBytes = c.stats.evictedBytes.Load()
	snapshot.Promotions = c.stats.promotions.Load()
	snapshot.WriteQueueDropped = c.stats.writeDropped.Load()
	snapshot.LastWriteMS = c.stats.lastWriteMS.Load()
	snapshot.MaxWriteMS = c.stats.maxWriteMS.Load()
	snapshot.LastWriteQueueMS = c.stats.lastWriteQueueMS.Load()
	snapshot.MaxWriteQueueMS = c.stats.maxWriteQueueMS.Load()
	snapshot.LastGetError = c.lastGetError
	if !c.lastGetAt.IsZero() {
		at := c.lastGetAt
		snapshot.LastGetErrorAt = &at
	}
	snapshot.LastPutError = c.lastPutError
	if !c.lastPutAt.IsZero() {
		at := c.lastPutAt
		snapshot.LastPutErrorAt = &at
	}
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		for fid, fc := range sh.chunks {
			fc.mu.RLock()
			// The class is decided by the file's total cached bytes, so the
			// unproven total has to be known before the class is.
			var cachedBytes, unproven int64
			for _, chunk := range fc.chunks {
				cachedBytes += chunk.size
				if chunk.state == stateUnproven {
					unproven += chunk.size
				}
			}
			file := DebugReadCacheFile{ID: fid, Size: fc.fileSize}
			for _, chunk := range fc.chunks {
				snapshot.ChunkCount++
				snapshot.Bytes += chunk.size
				file.ChunkCount++
				file.Bytes += chunk.size
				switch chunk.state {
				case stateProtected:
					snapshot.ProtectedBytes += chunk.size
				case stateProbationary:
					snapshot.ProbationaryBytes += chunk.size
				}
			}
			snapshot.UnprovenBytes += unproven
			file.Large = classOf(file.Size, cachedBytes) == classLarge
			if file.Large {
				snapshot.StreamBytes += unproven
				snapshot.LargeFileBytes += file.Bytes
			} else {
				snapshot.SmallFileBytes += file.Bytes
			}
			if file.ChunkCount > 0 {
				snapshot.Files = append(snapshot.Files, file)
			}
			fc.mu.RUnlock()
		}
		sh.mu.RUnlock()
	}
	sort.Slice(snapshot.Files, func(i, j int) bool {
		return snapshot.Files[i].ID < snapshot.Files[j].ID
	})
	snapshot.ProvenBytes = snapshot.ProbationaryBytes + snapshot.ProtectedBytes
	return snapshot
}
