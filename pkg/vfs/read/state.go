package read

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
)

// Read-domain constants.
const (
	ChunkSize        = 1024 * 1024
	PrefetchLimit    = 2
	PrefetchChunks   = 8
	MaxConcurrency   = 8
	HighReserve      = 2
	HotChunkLimit    = 16
	RangeHitLimit    = 1024
	RangePromoteHits = 2
	HistoryLimit     = 1000
)

// windowLoad coalesces concurrent reads of the same cache window.
// mu guards loads; entries are removed when their window load finishes.
type windowLoad struct {
	fid   string
	start int64
	end   int64
	done  chan struct{}
	data  map[int64][]byte
	extra map[string]any
	err   error
}

// slotState bounds concurrent driver reads. The normal/high channels are
// the lock itself - no additional mutex. Lifecycle: created in NewState,
// never closed (workers select on the VFS context instead).
type slotState struct {
	normal chan struct{}
	high   chan struct{}
}

func newSlotState() *slotState {
	return &slotState{
		normal: make(chan struct{}, MaxConcurrency-HighReserve),
		high:   make(chan struct{}, HighReserve),
	}
}

// windowState coalesces concurrent reads of the same cache window.
// Owned by the read domain; mu guards loads.
type windowState struct {
	mu    sync.Mutex
	loads map[string]*windowLoad
}

func newWindowState() *windowState {
	return &windowState{
		loads: map[string]*windowLoad{},
	}
}

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

// prefetchState tracks in-flight window prefetches.
type prefetchState struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	sem      chan struct{}
}

func newPrefetchState() *prefetchState {
	return &prefetchState{
		inFlight: map[string]struct{}{},
		sem:      make(chan struct{}, PrefetchLimit),
	}
}

// historyState is the bounded read-event ring for debug snapshots.
// mu guards events/pos/count/sequence.
type historyState struct {
	mu       sync.Mutex
	events   []drive.MetricEvent // ring buffer; lazily grown toward HistoryLimit
	pos      int                 // ring index of the next write slot
	count    int                 // number of live events (<= cap(events))
	sequence uint64
}

func newHistoryState() *historyState {
	return &historyState{}
}

// NextSequence returns the next read-op sequence number.
func (h *historyState) NextSequence() uint64 {
	return atomic.AddUint64(&h.sequence, 1)
}

// Append records one read event, growing the ring toward HistoryLimit.
func (h *historyState) Append(event drive.MetricEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.events == nil {
		// Grow lazily toward the limit so idle VFS instances do not
		// preallocate a full ring.
		size := 64
		if HistoryLimit < size {
			size = HistoryLimit
		}
		h.events = make([]drive.MetricEvent, size)
	}
	if h.count == len(h.events) {
		if len(h.events) < HistoryLimit {
			// Ring full but not at the limit yet: double, preserving order.
			size := len(h.events) * 2
			if size > HistoryLimit {
				size = HistoryLimit
			}
			next := make([]drive.MetricEvent, size)
			for i := 0; i < h.count; i++ {
				next[i] = h.events[(h.pos-h.count+i+len(h.events))%len(h.events)]
			}
			h.events = next
			h.pos = h.count
		}
	}
	h.events[h.pos] = event
	h.pos = (h.pos + 1) % len(h.events)
	if h.count < len(h.events) {
		h.count++
	}
}

// Snapshot returns live events in chronological order.
func (h *historyState) Snapshot() []drive.MetricEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return nil
	}
	out := make([]drive.MetricEvent, h.count)
	for i := 0; i < h.count; i++ {
		out[i] = h.events[(h.pos-h.count+i+len(h.events))%len(h.events)]
	}
	return out
}

// State groups the read-domain state so ownership is explicit: the fields
// are initialized together, touched only by read paths, and shut down by
// the VFS lifecycle.
type State struct {
	cache    Cache
	history  *historyState
	prefetch *prefetchState
	slots    *slotState
	fastPath *fastPathState
	windows  *windowState
}

// NewState builds the read domain state together so ownership and
// initialization stay in one place. cache may be nil (nil-safe: read
// paths fall back to no caching).
func NewState(cache Cache) *State {
	return &State{
		cache:    cache,
		history:  newHistoryState(),
		prefetch: newPrefetchState(),
		slots:    newSlotState(),
		fastPath: newFastPathState(),
		windows:  newWindowState(),
	}
}

// Cache returns the durable chunk store (nil when disabled).
func (s *State) Cache() Cache { return s.cache }

// History returns the read-event ring for debug snapshots.
//
//nolint:revive // debug handle asserted in pkg/vfs lifecycle tests; the concrete type stays internal
func (s *State) History() *historyState { return s.history }

// Close stops the durable read-cache writer and waits for pending writes.
// Safe on a zero State (hand-constructed VFS values in tests may have no
// cache).
func (s *State) Close() error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.Close()
}

// --- state methods (previously vfsReadRuntime) ---

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

func (s *State) beginWindowLoad(key string, load *windowLoad) (*windowLoad, bool) {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	if existing := s.windows.loads[key]; existing != nil {
		return existing, true
	}
	s.windows.loads[key] = load
	return load, false
}

func (s *State) endWindowLoad(key string) {
	s.windows.mu.Lock()
	delete(s.windows.loads, key)
	s.windows.mu.Unlock()
}

func (s *State) findWindow(cacheKey string, index int64) *windowLoad {
	s.windows.mu.Lock()
	defer s.windows.mu.Unlock()
	for _, candidate := range s.windows.loads {
		if candidate.fid == cacheKey && index >= candidate.start && index <= candidate.end {
			return candidate
		}
	}
	return nil
}

func (s *State) windowContains(cacheKey string, index int64) bool {
	return s.findWindow(cacheKey, index) != nil
}

func (s *State) reservePrefetch(key string) bool {
	s.prefetch.mu.Lock()
	if _, ok := s.prefetch.inFlight[key]; ok {
		s.prefetch.mu.Unlock()
		return false
	}
	s.prefetch.inFlight[key] = struct{}{}
	s.prefetch.mu.Unlock()

	select {
	case s.prefetch.sem <- struct{}{}:
		return true
	default:
		s.prefetch.mu.Lock()
		delete(s.prefetch.inFlight, key)
		s.prefetch.mu.Unlock()
		return false
	}
}

func (s *State) releasePrefetch(key string) {
	<-s.prefetch.sem
	s.prefetch.mu.Lock()
	delete(s.prefetch.inFlight, key)
	s.prefetch.mu.Unlock()
}

// NextSequence returns the next read-op sequence number.
func (s *State) NextSequence() uint64 {
	return s.history.NextSequence()
}

// AppendHistory records one read event.
func (s *State) AppendHistory(event drive.MetricEvent) {
	s.history.Append(event)
}

// HistorySnapshot returns live read events in chronological order.
func (s *State) HistorySnapshot() []drive.MetricEvent {
	return s.history.Snapshot()
}

// ResetHistory clears the read-event ring.
func (s *State) ResetHistory() {
	s.history.mu.Lock()
	s.history.events = nil
	s.history.pos = 0
	s.history.count = 0
	s.history.mu.Unlock()
}

// RuntimeStats reports window/prefetch/range-hit counters for debug.
func (s *State) RuntimeStats() (windowLoads, prefetches, rangeHits int) {
	s.windows.mu.Lock()
	windowLoads = len(s.windows.loads)
	s.windows.mu.Unlock()
	s.prefetch.mu.Lock()
	prefetches = len(s.prefetch.inFlight)
	s.prefetch.mu.Unlock()
	s.fastPath.rangeHit.mu.Lock()
	rangeHits = len(s.fastPath.rangeHit.hits)
	s.fastPath.rangeHit.mu.Unlock()
	return windowLoads, prefetches, rangeHits
}

// FlushReadCache flushes the durable read cache.
func (s *State) FlushReadCache() error {
	if s.cache == nil {
		return nil
	}
	return s.cache.FlushReadCache()
}

// ClearReadCache clears durable and in-memory read-cache entries.
func (s *State) ClearReadCache() error {
	s.fastPath.hot.mu.Lock()
	s.fastPath.hot.chunks = map[string][]byte{}
	s.fastPath.hot.lru = nil
	s.fastPath.hot.mu.Unlock()
	s.fastPath.rangeHit.mu.Lock()
	s.fastPath.rangeHit.hits = map[string]int{}
	s.fastPath.rangeHit.lru = nil
	s.fastPath.rangeHit.mu.Unlock()
	if s.cache == nil {
		return nil
	}
	return s.cache.ClearReadCache()
}

// InvalidateFile drops cached chunks for one file id.
func (s *State) InvalidateFile(fid string) {
	if s.cache != nil {
		s.cache.InvalidateFile(fid)
	}
}

// PutLocalFile seeds the cache from a local file.
func (s *State) PutLocalFile(fid string, fileSize int64, localPath string) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.PutLocalFile(fid, fileSize, localPath)
}

// PutReader seeds the cache from reader content.
func (s *State) PutReader(fid string, fileSize int64, r io.Reader) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.PutReader(fid, fileSize, r)
}

// DebugSnapshot returns the read cache debug snapshot.
func (s *State) DebugSnapshot() readcache.DebugReadCache {
	if s.cache == nil {
		return readcache.DebugReadCache{}
	}
	return s.cache.DebugSnapshot()
}

// StatesReady reports whether the runtime sub-states are initialized.
func (s *State) StatesReady() bool {
	return s != nil && s.prefetch != nil && s.slots != nil && s.fastPath != nil && s.windows != nil
}
