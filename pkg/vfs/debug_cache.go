package vfs

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
)

func (c *readCacheStore) debugReadCache() DebugReadCache {
	snapshot := DebugReadCache{MaxBytes: c.maxSize, LargeFileThreshold: readCacheLargeFileBytes}
	snapshot.WriteQueueLength = len(c.cacheWriteQueue)
	snapshot.WriteQueueCap = cap(c.cacheWriteQueue)
	snapshot.WriteQueueBytes = c.cacheWriteBytes.Load()
	snapshot.WriteQueueMaxBytes = int64(cap(c.cacheWriteQueue)) * readChunkSize
	c.readIndexMu.Lock()
	snapshot.IndexDirty = c.readIndexDirty
	snapshot.IndexFlushScheduled = c.readIndexTimer != nil
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
			file := DebugReadCacheFile{ID: fid, Size: fc.fileSize}
			for _, chunk := range fc.chunks {
				snapshot.ChunkCount++
				snapshot.Bytes += chunk.size
				file.ChunkCount++
				file.Bytes += chunk.size
			}
			file.Large = readCacheFileLarge(file.Size, file.Bytes)
			if file.Large {
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
	return snapshot
}

func (c *Stores) DebugReadCacheForTest() DebugReadCache {
	snapshot := c.readCacheStore.debugReadCache()
	snapshot.Journal = c.uploadStore.debugJournal()
	return snapshot
}

func (c *uploadStore) debugJournal() *DebugJournal {
	path := c.journalPath()
	journal := &DebugJournal{Path: path}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		c.mu.RLock()
		journal.PendingCount = len(c.pending)
		c.mu.RUnlock()
		return journal
	}
	if err != nil {
		journal.Error = err.Error()
		return journal
	}
	journal.Exists = true
	journal.Bytes = info.Size()

	f, err := os.Open(path)
	if err != nil {
		journal.Error = err.Error()
		return journal
	}
	defer f.Close()

	type pathState struct {
		item DebugJournalPath
	}
	paths := map[string]*pathState{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	validEntries := 0
	for scanner.Scan() {
		line++
		journal.Entries++
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			journal.InvalidEntries++
			continue
		}
		if entry.Path == "" {
			journal.InvalidEntries++
			continue
		}
		state := paths[entry.Path]
		if state == nil {
			state = &pathState{item: DebugJournalPath{Path: entry.Path}}
			paths[entry.Path] = state
		}
		validEntries++
		state.item.Entries++
		state.item.LastJournalOp = entry.Op
		state.item.LastJournalLine = line
		if entry.Op == "dirty" {
			state.item.LatestSize = entry.Size
			state.item.LastError = entry.LastError
		}
	}
	if err := scanner.Err(); err != nil {
		journal.Error = err.Error()
	}

	c.mu.RLock()
	journal.PendingCount = len(c.pending)
	pending := make(map[string]PendingUpload, len(c.pending))
	for path, item := range c.pending {
		pending[path] = item
	}
	c.mu.RUnlock()

	journal.UniquePaths = len(paths)
	if validEntries > journal.UniquePaths {
		journal.DuplicateEntries = validEntries - journal.UniquePaths
	}
	journal.CompactRecommended = c.shouldCompactJournal(journal.Entries)

	for path, state := range paths {
		if state.item.Entries > 1 {
			state.item.DuplicateEntries = state.item.Entries - 1
		}
		if p, ok := pending[path]; ok {
			state.item.LatestSize = p.Size
			state.item.LastError = p.LastError
			if info, err := os.Stat(p.LocalPath); err == nil {
				state.item.StagingExists = true
				state.item.StagingSize = info.Size()
				state.item.SizeMatches = info.Size() == p.Size
			}
		}
		journal.LargestPaths = append(journal.LargestPaths, state.item)
	}
	sort.Slice(journal.LargestPaths, func(i, j int) bool {
		if journal.LargestPaths[i].Entries == journal.LargestPaths[j].Entries {
			return journal.LargestPaths[i].Path < journal.LargestPaths[j].Path
		}
		return journal.LargestPaths[i].Entries > journal.LargestPaths[j].Entries
	})
	if len(journal.LargestPaths) > 10 {
		journal.LargestPaths = journal.LargestPaths[:10]
	}
	return journal
}

type debugCacheRuntime interface {
	ReadCache() DebugReadCache
	Journal() *DebugJournal
}

type vfsDebugCacheRuntime struct {
	v *VFS
}

func newVFSDebugCacheRuntime(v *VFS) vfsDebugCacheRuntime {
	return vfsDebugCacheRuntime{v: v}
}

func (r vfsDebugCacheRuntime) ReadCache() DebugReadCache {
	return r.v.readCache.debugReadCache()
}

func (r vfsDebugCacheRuntime) Journal() *DebugJournal {
	return r.v.uploads.debugJournal()
}

func debugCacheSnapshotWithRuntime(runtime debugCacheRuntime) DebugReadCache {
	snapshot := runtime.ReadCache()
	snapshot.Journal = runtime.Journal()
	return snapshot
}
