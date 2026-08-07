package vfs

import (
	"hash/fnv"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const entryShards = 16

type entryShard struct {
	mu      sync.RWMutex
	entries map[string]drive.Entry
}

// shardedEntryMap replaces a single map[string]drive.Entry guarded by one
// sync.RWMutex. FUSE Lookup/GetAttr/ReadDirPlus all hit entries[path]
// on every metadata operation; sharding by path eliminates the single global
// lock that contended across all concurrent lookups.
type shardedEntryMap struct {
	shards [entryShards]entryShard
}

func newShardedEntryMap() *shardedEntryMap {
	m := &shardedEntryMap{}
	for i := range m.shards {
		m.shards[i].entries = make(map[string]drive.Entry)
	}
	return m
}

func (m *shardedEntryMap) shardFor(path string) *entryShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return &m.shards[h.Sum32()%entryShards]
}

// Get returns the entry at path, or false if absent. One shard RLock.
func (m *shardedEntryMap) Get(path string) (drive.Entry, bool) {
	s := m.shardFor(path)
	s.mu.RLock()
	e, ok := s.entries[path]
	s.mu.RUnlock()
	return e, ok
}

// Set stores entry at path. One shard Lock.
func (m *shardedEntryMap) Set(path string, entry drive.Entry) {
	s := m.shardFor(path)
	s.mu.Lock()
	s.entries[path] = entry
	s.mu.Unlock()
}

// Delete removes the entry at path. One shard Lock.
func (m *shardedEntryMap) Delete(path string) {
	s := m.shardFor(path)
	s.mu.Lock()
	delete(s.entries, path)
	s.mu.Unlock()
}

// DeleteUnder removes all entries whose path equals prefix or is under it.
// All shards are locked sequentially; this is a rare operation (rename,
// overlay restore).
func (m *shardedEntryMap) DeleteUnder(prefix string) {
	for i := range m.shards {
		m.shards[i].mu.Lock()
		for path := range m.shards[i].entries {
			if path == prefix || isPathUnder(path, prefix) {
				delete(m.shards[i].entries, path)
			}
		}
		m.shards[i].mu.Unlock()
	}
}

// Range iterates all entries. Entries are collected under shard locks first;
// the callback runs with no locks held. This allows callers to safely mutate
// entries within the callback (Get, Set, Delete, etc.).
func (m *shardedEntryMap) Range(fn func(path string, entry drive.Entry) bool) {
	// Collect entries under each shard lock.
	type pair struct {
		path  string
		entry drive.Entry
	}
	var buf []pair
	for i := range m.shards {
		m.shards[i].mu.RLock()
		for path, entry := range m.shards[i].entries {
			buf = append(buf, pair{path, entry})
		}
		m.shards[i].mu.RUnlock()
	}
	for _, p := range buf {
		if !fn(p.path, p.entry) {
			return
		}
	}
}

// Len returns the total entry count across all shards.
func (m *shardedEntryMap) Len() int {
	n := 0
	for i := range m.shards {
		m.shards[i].mu.RLock()
		n += len(m.shards[i].entries)
		m.shards[i].mu.RUnlock()
	}
	return n
}
