package read

import (
	"strings"
	"sync"
)

// Hot-chunk fast path: blocks held in memory until their async durable
// cache write lands, plus range-hit promotion tracking.

// hotChunkState is the in-memory hot-chunk cache (fast path around the
// durable read cache). mu guards chunks/lru.
type hotChunkState struct {
	mu     sync.Mutex
	chunks map[string][]byte
	lru    []string
}

type rangeHitState struct {
	mu   sync.Mutex
	hits map[string]int
	lru  []string
}

type fastPathState struct {
	hot      hotChunkState
	rangeHit rangeHitState
}

func newFastPathState() *fastPathState {
	return &fastPathState{
		hot: hotChunkState{
			chunks: map[string][]byte{},
		},
		rangeHit: rangeHitState{
			hits: map[string]int{},
		},
	}
}

func (s *State) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	key := ChunkKey(cacheKey, index)
	s.fastPath.hot.mu.Lock()
	defer s.fastPath.hot.mu.Unlock()
	data, ok := s.fastPath.hot.chunks[key]
	if !ok {
		return nil, false
	}
	for i, candidate := range s.fastPath.hot.lru {
		if candidate == key {
			copy(s.fastPath.hot.lru[i:], s.fastPath.hot.lru[i+1:])
			s.fastPath.hot.lru[len(s.fastPath.hot.lru)-1] = key
			break
		}
	}
	return data, true
}

// peekHotChunk reports residency without touching recency; diagnostics use it
// so a snapshot cannot change eviction order.
func (s *State) peekHotChunk(cacheKey string, index int64) bool {
	key := ChunkKey(cacheKey, index)
	s.fastPath.hot.mu.Lock()
	defer s.fastPath.hot.mu.Unlock()
	_, ok := s.fastPath.hot.chunks[key]
	return ok
}

func (s *State) putHotChunk(cacheKey string, index int64, data []byte) {
	key := ChunkKey(cacheKey, index)
	s.fastPath.hot.mu.Lock()
	defer s.fastPath.hot.mu.Unlock()
	if _, ok := s.fastPath.hot.chunks[key]; !ok {
		s.fastPath.hot.lru = append(s.fastPath.hot.lru, key)
	}
	s.fastPath.hot.chunks[key] = data
	for len(s.fastPath.hot.lru) > HotChunkLimit {
		oldest := s.fastPath.hot.lru[0]
		s.fastPath.hot.lru = s.fastPath.hot.lru[1:]
		delete(s.fastPath.hot.chunks, oldest)
	}
}

func (s *State) shouldPromoteCachedRange(cacheKey string, index int64) bool {
	key := ChunkKey(cacheKey, index)
	s.fastPath.rangeHit.mu.Lock()
	defer s.fastPath.rangeHit.mu.Unlock()
	hits := s.fastPath.rangeHit.hits[key]
	if hits+1 < RangePromoteHits {
		return false
	}
	delete(s.fastPath.rangeHit.hits, key)
	for i, candidate := range s.fastPath.rangeHit.lru {
		if candidate == key {
			s.fastPath.rangeHit.lru = append(s.fastPath.rangeHit.lru[:i], s.fastPath.rangeHit.lru[i+1:]...)
			break
		}
	}
	return true
}

func (s *State) recordCachedRangeHit(cacheKey string, index, requestSize int64) {
	if !shouldPromoteCachedRange(requestSize) {
		return
	}
	key := ChunkKey(cacheKey, index)
	s.fastPath.rangeHit.mu.Lock()
	defer s.fastPath.rangeHit.mu.Unlock()
	if _, ok := s.fastPath.rangeHit.hits[key]; !ok {
		s.fastPath.rangeHit.lru = append(s.fastPath.rangeHit.lru, key)
	}
	s.fastPath.rangeHit.hits[key]++
	for len(s.fastPath.rangeHit.lru) > RangeHitLimit {
		oldest := s.fastPath.rangeHit.lru[0]
		s.fastPath.rangeHit.lru = s.fastPath.rangeHit.lru[1:]
		delete(s.fastPath.rangeHit.hits, oldest)
	}
}

// HotChunkStats returns the hot-chunk entry count and total bytes.
func (s *State) HotChunkStats() (int, int64) {
	s.fastPath.hot.mu.Lock()
	defer s.fastPath.hot.mu.Unlock()
	var bytes int64
	for _, data := range s.fastPath.hot.chunks {
		bytes += int64(len(data))
	}
	return len(s.fastPath.hot.chunks), bytes
}

// dropFastPath removes one file's hot chunks and range-hit counters. The hot
// chunk key is fid + NUL + chunk index, so a byte-wise prefix match cannot
// collide with another file id.
func (s *State) dropFastPath(fid string) {
	prefix := fid + "\x00"
	s.fastPath.hot.mu.Lock()
	keptChunks := s.fastPath.hot.chunks
	if len(keptChunks) > 0 {
		s.fastPath.hot.chunks = make(map[string][]byte, len(keptChunks))
	}
	keptLRU := s.fastPath.hot.lru[:0]
	for _, key := range s.fastPath.hot.lru {
		if strings.HasPrefix(key, prefix) {
			delete(keptChunks, key)
			continue
		}
		keptLRU = append(keptLRU, key)
		if data, ok := keptChunks[key]; ok {
			s.fastPath.hot.chunks[key] = data
		}
	}
	s.fastPath.hot.lru = keptLRU
	s.fastPath.hot.mu.Unlock()

	s.fastPath.rangeHit.mu.Lock()
	keptHits := s.fastPath.rangeHit.hits
	if len(keptHits) > 0 {
		s.fastPath.rangeHit.hits = make(map[string]int, len(keptHits))
	}
	keptRangeLRU := s.fastPath.rangeHit.lru[:0]
	for _, key := range s.fastPath.rangeHit.lru {
		if strings.HasPrefix(key, prefix) {
			delete(keptHits, key)
			continue
		}
		keptRangeLRU = append(keptRangeLRU, key)
		if hits, ok := keptHits[key]; ok {
			s.fastPath.rangeHit.hits[key] = hits
		}
	}
	s.fastPath.rangeHit.lru = keptRangeLRU
	s.fastPath.rangeHit.mu.Unlock()
}
