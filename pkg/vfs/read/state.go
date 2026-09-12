package read

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
)

// Read-domain constants.
const (
	// ChunkSize 是 VFS 读取、缓存和预取使用的基础文件块大小。
	ChunkSize = 512 * 1024
	// PrefetchChunks 是普通读取首次或非连续读取使用的预取块数量。
	PrefetchChunks = 8
	// MaxConcurrency 是 VFS 读取任务允许占用的并发槽位总数。
	MaxConcurrency = 32
	// HighReserve 根据总并发数自动计算，为高优先级读取预留约四分之一的槽位，
	// 同时保证总并发数大于 1 时至少保留一个槽位。
	HighReserve = max(0, min(max(1, MaxConcurrency/4), MaxConcurrency-1))
	// PrefetchLimit 根据普通读取槽位自动计算，并限制单个文件的预取窗口上限。
	// 预取不能占满所有普通读取槽位，否则多个文件同时访问时会影响前台读取。
	PrefetchLimit = max(1, min(4, MaxConcurrency-HighReserve))
	// HotChunkLimit 是单个 VFS 实例保留在内存热缓存中的最大块数量。
	HotChunkLimit = 96
	// RangeHitLimit 是范围命中统计允许保留的最大条目数量。
	RangeHitLimit = 1024
	// RangePromoteHits 是把重复范围访问提升为热缓存访问所需的命中次数，
	// 不超过范围命中统计的容量。
	RangePromoteHits = min(2, RangeHitLimit)
	// SequentialLimit 是单个文件访问序列跟踪允许保留的最大记录数量。
	SequentialLimit = 1024
	// SequentialPrefetchChunks 根据普通预取块数自动计算，确认顺序读取后每次预取的块数量。
	SequentialPrefetchChunks = max(1, PrefetchChunks*2)
	// SummaryHistoryLimit 是"每次读取一条"的汇总事件保留量。它保证最近
	// SummaryHistoryLimit 次读取的汇总始终可见。
	SummaryHistoryLimit = 128
	// DetailHistoryLimit 是阶段与分块明细事件的保留量。明细按最近优先：
	// 一次整文件读取就能写满它，因此更早的明细会被冲掉，而汇总不受影响。
	DetailHistoryLimit = 512
	// 两份历史的内存上界约为 (SummaryHistoryLimit+DetailHistoryLimit) × 512 B
	// ≈ 323 KiB/挂载：drive.MetricEvent 实测 504 B，加每条 8 B 写入序号。
	// 环形缓冲从 64 起按需倍增，从未发生过读取的挂载不预付这份内存。
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
	mu              sync.Mutex
	inFlight        map[string]struct{}
	cancels         map[string]map[string]context.CancelFunc
	sem             chan struct{}
	foregroundReads atomic.Int32
}

type sequentialRead struct {
	end       int64
	lastChunk int64
	confirmed bool
	requestID uint64
}

type sequentialKey struct {
	cacheKey string
	session  uint64
}

type accessDecision struct {
	sequential    bool
	stale         bool
	adaptive      bool
	discontinuous bool
}

// sequentialState tracks recent per-file access patterns. It is only a
// prefetch hint: concurrent or jumping readers reset the hint without
// affecting read correctness.
type sequentialState struct {
	mu    sync.Mutex
	reads map[sequentialKey]sequentialRead
	order []sequentialKey
}

func newSequentialState() *sequentialState {
	return &sequentialState{reads: map[sequentialKey]sequentialRead{}}
}

func newPrefetchState() *prefetchState {
	return &prefetchState{
		inFlight: map[string]struct{}{},
		cancels:  map[string]map[string]context.CancelFunc{},
		sem:      make(chan struct{}, PrefetchLimit),
	}
}

// historyEntry pairs a debug event with its append order so the two rings
// can be merged back into one chronological list.
type historyEntry struct {
	seq   uint64
	event drive.MetricEvent
}

// eventRing is a bounded FIFO of history entries, grown lazily toward limit
// so mounts that never read do not preallocate a full ring.
type eventRing struct {
	entries []historyEntry // lazily grown toward limit
	pos     int            // ring index of the next write slot
	count   int            // number of live entries (<= len(entries))
}

// append records one entry, growing the ring toward limit.
func (r *eventRing) append(seq uint64, event drive.MetricEvent, limit int) {
	if limit < 1 {
		return
	}
	if r.entries == nil {
		r.entries = make([]historyEntry, min(64, limit))
	}
	if r.count == len(r.entries) && len(r.entries) < limit {
		// Ring full but not at the limit yet: double, preserving order.
		next := make([]historyEntry, min(2*len(r.entries), limit))
		for i := 0; i < r.count; i++ {
			next[i] = r.entries[(r.pos-r.count+i+len(r.entries))%len(r.entries)]
		}
		r.entries = next
		r.pos = r.count
	}
	r.entries[r.pos] = historyEntry{seq: seq, event: event}
	r.pos = (r.pos + 1) % len(r.entries)
	if r.count < len(r.entries) {
		r.count++
	}
}

// snapshot returns live entries in append order.
func (r *eventRing) snapshot() []historyEntry {
	if r.count == 0 {
		return nil
	}
	out := make([]historyEntry, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.entries[(r.pos-r.count+i+len(r.entries))%len(r.entries)]
	}
	return out
}

// historyState is the bounded read-event history for debug snapshots: a
// summary ring (one event per read) and a detail ring (per chunk and per
// window), merged by append order when read. Keeping the two apart makes
// "a slow read's detail burst cannot evict earlier reads' summaries" a
// structural property instead of a capacity guess.
type historyState struct {
	mu        sync.Mutex
	summaries eventRing
	details   eventRing
	appendSeq uint64
	opSeq     uint64 // read-op sequence, atomic (NextSequence)
}

func newHistoryState() *historyState {
	return &historyState{}
}

// NextSequence returns the next read-op sequence number.
func (h *historyState) NextSequence() uint64 {
	return atomic.AddUint64(&h.opSeq, 1)
}

// AppendSummary records one read-summary event.
func (h *historyState) AppendSummary(event drive.MetricEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendSeq++
	h.summaries.append(h.appendSeq, event, SummaryHistoryLimit)
}

// AppendDetail records one phase or per-chunk detail event.
func (h *historyState) AppendDetail(event drive.MetricEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendSeq++
	h.details.append(h.appendSeq, event, DetailHistoryLimit)
}

// Snapshot returns live events in append order, with summary and detail
// events interleaved by when they were recorded.
func (h *historyState) Snapshot() []drive.MetricEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	summaries := h.summaries.snapshot()
	details := h.details.snapshot()
	if len(summaries) == 0 && len(details) == 0 {
		return nil
	}
	out := make([]drive.MetricEvent, 0, len(summaries)+len(details))
	i, j := 0, 0
	for i < len(summaries) && j < len(details) {
		if summaries[i].seq < details[j].seq {
			out = append(out, summaries[i].event)
			i++
			continue
		}
		out = append(out, details[j].event)
		j++
	}
	for ; i < len(summaries); i++ {
		out = append(out, summaries[i].event)
	}
	for ; j < len(details); j++ {
		out = append(out, details[j].event)
	}
	return out
}

// reset clears both rings.
func (h *historyState) reset() {
	h.mu.Lock()
	h.summaries = eventRing{}
	h.details = eventRing{}
	h.mu.Unlock()
}

// State groups the read-domain state so ownership is explicit: the fields
// are initialized together, touched only by read paths, and shut down by
// the VFS lifecycle.
type State struct {
	cache    Cache
	history  *historyState
	prefetch *prefetchState
	sequence *sequentialState
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
		sequence: newSequentialState(),
		slots:    newSlotState(),
		fastPath: newFastPathState(),
		windows:  newWindowState(),
	}
}

func (s *State) observeSequentialRead(cacheKey string, offset, size int64) bool {
	return s.observeReadAccess(cacheKey, AccessHint{}, offset, size).sequential
}

func (s *State) observeReadAccess(cacheKey string, hint AccessHint, offset, size int64) accessDecision {
	adaptive := hint.SessionID != 0 && hint.RequestID != 0
	if cacheKey == "" || offset < 0 || size <= 0 {
		return accessDecision{adaptive: adaptive}
	}
	end := offset + size
	if end < offset {
		return accessDecision{adaptive: adaptive}
	}
	lastChunk := (end - 1) / ChunkSize
	key := sequentialKey{cacheKey: cacheKey}
	if adaptive {
		key.session = hint.SessionID
	}

	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	previous, exists := s.sequence.reads[key]
	if adaptive && exists && hint.RequestID <= previous.requestID {
		return accessDecision{stale: true, adaptive: true}
	}
	discontinuous := !exists || offset != previous.end
	confirmed := !hint.Concurrent && exists && previous.confirmed && offset == previous.end
	if !hint.Concurrent && exists && offset == previous.end && offset/ChunkSize > previous.lastChunk {
		confirmed = true
	}
	if !exists {
		s.sequence.order = append(s.sequence.order, key)
	}
	s.sequence.reads[key] = sequentialRead{
		end:       end,
		lastChunk: lastChunk,
		confirmed: confirmed,
		requestID: hint.RequestID,
	}
	for len(s.sequence.order) > SequentialLimit {
		oldest := s.sequence.order[0]
		s.sequence.order = s.sequence.order[1:]
		delete(s.sequence.reads, oldest)
	}
	return accessDecision{sequential: confirmed, adaptive: adaptive, discontinuous: discontinuous}
}

func (s *State) readAccessCurrent(cacheKey string, hint AccessHint) bool {
	if cacheKey == "" || hint.SessionID == 0 || hint.RequestID == 0 {
		return true
	}
	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	current, ok := s.sequence.reads[sequentialKey{cacheKey: cacheKey, session: hint.SessionID}]
	return ok && current.requestID == hint.RequestID
}

// ReleaseReadSession drops access-pattern hints for a closed open-file
// handle. In-flight reads remain valid; only future prefetch decisions lose
// the retired session history.
func (s *State) ReleaseReadSession(sessionID uint64) {
	if sessionID == 0 {
		return
	}
	s.sequence.mu.Lock()
	defer s.sequence.mu.Unlock()
	for key := range s.sequence.reads {
		if key.session == sessionID {
			delete(s.sequence.reads, key)
		}
	}
	order := s.sequence.order[:0]
	for _, key := range s.sequence.order {
		if key.session != sessionID {
			order = append(order, key)
		}
	}
	s.sequence.order = order
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

func (s *State) beginForegroundRead(cacheKey string) {
	if cacheKey != "" {
		s.prefetch.foregroundReads.Add(1)
	}
}

func (s *State) endForegroundRead(cacheKey string) {
	if cacheKey != "" {
		s.prefetch.foregroundReads.Add(-1)
	}
}

// adaptivePrefetchLimit keeps a small lookahead during active foreground
// reads and allows a wider pipeline when the reader is otherwise idle.
func (s *State) adaptivePrefetchLimit(sequential bool) int {
	limit := PrefetchLimit
	if s.prefetch.foregroundReads.Load() > int32(max(1, (MaxConcurrency-HighReserve)/2)) {
		return 1
	}
	// Keep the configured pipeline for both initial and confirmed sequential
	// reads while the foreground is idle. The sequential flag is retained in
	// the API for future bandwidth-aware policies.
	_ = sequential
	return limit
}

func (s *State) registerPrefetch(cacheKey, key string, cancel context.CancelFunc) {
	s.prefetch.mu.Lock()
	defer s.prefetch.mu.Unlock()
	if s.prefetch.cancels[cacheKey] == nil {
		s.prefetch.cancels[cacheKey] = map[string]context.CancelFunc{}
	}
	s.prefetch.cancels[cacheKey][key] = cancel
}

func (s *State) unregisterPrefetch(cacheKey, key string) {
	s.prefetch.mu.Lock()
	defer s.prefetch.mu.Unlock()
	if tasks := s.prefetch.cancels[cacheKey]; tasks != nil {
		delete(tasks, key)
		if len(tasks) == 0 {
			delete(s.prefetch.cancels, cacheKey)
		}
	}
}

// cancelPrefetch cancels only speculative reads for one file. Foreground
// reads use their caller context and are never canceled by a seek elsewhere.
func (s *State) cancelPrefetch(cacheKey string) {
	s.prefetch.mu.Lock()
	tasks := s.prefetch.cancels[cacheKey]
	for _, cancel := range tasks {
		cancel()
	}
	s.prefetch.mu.Unlock()
}

// NextSequence returns the next read-op sequence number.
func (s *State) NextSequence() uint64 {
	return s.history.NextSequence()
}

// AppendHistory records one read event.
// AppendHistory records one read-summary debug event.
func (s *State) AppendHistory(event drive.MetricEvent) {
	s.history.AppendSummary(event)
}

// AppendDetailHistory records one read detail debug event (phase or chunk).
func (s *State) AppendDetailHistory(event drive.MetricEvent) {
	s.history.AppendDetail(event)
}

// HistorySnapshot returns live read events in append order.
func (s *State) HistorySnapshot() []drive.MetricEvent {
	return s.history.Snapshot()
}

// ResetHistory clears the read-event history.
func (s *State) ResetHistory() {
	s.history.reset()
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
	s.sequence.mu.Lock()
	s.sequence.reads = map[sequentialKey]sequentialRead{}
	s.sequence.order = nil
	s.sequence.mu.Unlock()
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
	return s != nil && s.prefetch != nil && s.sequence != nil && s.slots != nil && s.fastPath != nil && s.windows != nil
}
