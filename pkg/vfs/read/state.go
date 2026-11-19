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
	// ChunkSize 是 VFS 读取、缓存和预取使用的基础文件块大小，与读配置中的
	// 块单位一致。
	ChunkSize = drive.ReadChunkSize
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

// State groups the read-domain state so ownership is explicit: the fields
// are initialized together, touched only by read paths, and shut down by
// the VFS lifecycle.
type State struct {
	cache      Cache
	history    *historyState
	scheduler  *Scheduler
	sequence   *sequentialState
	fastPath   *fastPathState
	windows    *windowState
	scopes     *ScopeRegistry
	prefetchMu sync.Mutex // Serializes speculative range selection across sessions.
	timeouts   timeoutStats
	// override is a measurement-only plan override (probe test read). It is
	// nil in normal operation.
	override atomic.Pointer[PlanOverride]
}

// NewState builds the read domain state together so ownership and
// initialization stay in one place. cache may be nil (nil-safe: read
// paths fall back to no caching).
func NewState(cache Cache) *State {
	s := &State{
		cache:     cache,
		history:   newHistoryState(),
		scheduler: NewScheduler(DefaultSchedulerConfig()),
		sequence:  newSequentialState(),
		fastPath:  newFastPathState(),
		windows:   newWindowState(),
		scopes:    newScopeRegistry(),
	}
	s.scopes.onChange = s.quiesceSignal
	return s
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
	if s == nil {
		return nil
	}
	s.cancelAllScopes("close")
	if s.cache == nil {
		return nil
	}
	return s.cache.Close()
}

// CloseReadCache closes the durable read-cache writer and cancels speculative
// work while keeping the read path usable: a later cache-miss read starts a
// fresh scope and fetches from the provider. Only Close (VFS teardown) makes
// the scope registry permanently refuse new scopes.
func (s *State) CloseReadCache() error {
	if s == nil {
		return nil
	}
	s.cancelAllScopes("cache_close")
	if s.cache == nil {
		return nil
	}
	return s.cache.Close()
}

// RuntimeStats reports window/prefetch/range-hit counters for debug.
func (s *State) RuntimeStats() (windowLoads, prefetches, rangeHits int) {
	s.windows.mu.Lock()
	windowLoads = len(s.windows.loads)
	s.windows.mu.Unlock()
	prefetches = s.scheduler.Stats().PrefetchInUse
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
	s.cancelAllScopes("cache_clear")
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

// InvalidateFile drops cached chunks for one file id and cancels in-flight
// reads for it (glossary: 读缓存失效).
func (s *State) InvalidateFile(fid string) {
	s.cancelScopesByCacheKey(fid, "invalidate")
	s.DropFileCache(fid)
}

// DropFileCache drops cached chunks and access hints for one file id without
// cancelling in-flight reads. Debug invalidation uses it so making a file cold
// does not abort a read that is already running.
func (s *State) DropFileCache(fid string) {
	s.dropFastPath(fid)
	s.dropSequential(fid)
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
	return s != nil && s.scheduler != nil && s.sequence != nil && s.fastPath != nil && s.windows != nil && s.scopes != nil
}
