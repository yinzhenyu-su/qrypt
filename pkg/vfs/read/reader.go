package read

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"github.com/yinzhenyu/qrypt/pkg/vfs/readcache"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

// Reader implements the VFS read domain on top of a Host. The public
// methods (Read, ReadStream) are the same surfaces the VFS exposes; the
// unexported helpers mirror the old VFS methods.
type Reader struct {
	host     Host
	state    *State
	observer ReadObserver
	health   HealthRecorder
	counters CounterRecorder
	profile  drive.ReadPolicy
	source   *drive.ReadSource
	policy   *CachePolicy
	faults   ReadFaultInjector

	// fgMu guards fgSplit: the per-object adaptive foreground-split state.
	fgMu       sync.Mutex
	fgSplit    map[string]*fgSplitState
	fgLastSlow time.Time
}

// ReaderDeps is the explicit dependency set for a read-domain reader.
// Observer, Health and Counters are optional: nil values fall back to no-op
// sinks so instrumentation and statistics never affect correctness. Keeping
// every dependency in one struct (rather than positional arguments) lets the
// set grow without reordering call sites.
type ReaderDeps struct {
	Host     Host
	State    *State
	Observer ReadObserver
	Health   HealthRecorder
	Counters CounterRecorder
	// Source is the mounted execution runtime. Policy resolution and ownership
	// are settled by the composition root before constructing a Reader.
	Source *drive.ReadSource
	// ReadFaults is the probe-only fault registry. Nil disables injection.
	ReadFaults ReadFaultInjector
}

// NewReader builds a read-domain reader from explicit dependencies. The
// observer and health recorder are injected independently of the host
// surface, so an accidental implementation on the host cannot silently
// enable diagnostics or statistics.
func NewReader(deps ReaderDeps) *Reader {
	if deps.Observer == nil {
		deps.Observer = noopObserver{}
	}
	if deps.Health == nil {
		deps.Health = noopHealth{}
	}
	if deps.Counters == nil {
		deps.Counters = noopCounters{}
	}
	if deps.Source == nil {
		panic("read: source is required")
	}
	deps.State.scheduler = deps.Source.Scheduler()
	r := &Reader{
		host:     deps.Host,
		state:    deps.State,
		observer: deps.Observer,
		health:   deps.Health,
		counters: deps.Counters,
		profile:  deps.Source.Policy().Effective,
		source:   deps.Source,
		faults:   deps.ReadFaults,
		fgSplit:  map[string]*fgSplitState{},
	}
	r.policy = newCachePolicy(deps.State)
	return r
}

// State returns the read domain state.
func (r *Reader) State() *State { return r.state }

// ReleaseReadSession forgets access-pattern hints for a closed open-file
// handle. It does not cancel foreground reads or already useful cache fills.
func (r *Reader) ReleaseReadSession(sessionID uint64) {
	r.state.ReleaseReadSession(sessionID)
}

func (r *Reader) resolve(ctx context.Context, path string) (drive.Entry, error) {
	return r.host.Resolve(ctx, path)
}

// resolvePlan resolves the caller's intent, access stage, and the driver
// profile into the concrete plan carried by this read's context.
func (r *Reader) resolvePlan(ctx context.Context, defaultWindowChunks int, stage ReadStage) ReadPlan {
	hint, hinted := AccessHintFromContext(ctx)
	plan := PlanResolver{Profile: r.profile, AdaptiveDepth: r.state.adaptivePrefetchLimit(stage == StageSequential), ParallelSequential: hinted && !hint.Concurrent}.ResolveStage(stage, ReadIntentFromContext(ctx), defaultWindowChunks)
	r.applyPlanOverride(&plan)
	return plan
}

// stageFor classifies a read before its data is fetched. Only a hinted
// (open-file) session can be placed: an unhinted caller has no stream history,
// so it starts from the initial policy and is reclassified after the read.
func stageFor(hinted bool, hint AccessHint, decision accessDecision) ReadStage {
	if !hinted || hint.Concurrent || decision.stale {
		return StageInitial
	}
	switch {
	case decision.initial:
		return StageInitial
	case decision.discontinuous:
		return StageSeek
	case decision.sequential:
		return StageSequential
	default:
		return StageInitial
	}
}

// SetPlanOverride applies a measurement-only stream/window override to every
// read until ClearPlanOverride. It is wired to probe test read so benchmarks
// can compare configurations without rebuilding or remounting.
func (r *Reader) SetPlanOverride(override PlanOverride) {
	r.state.setPlanOverride(override)
}

// ClearPlanOverride removes the measurement override.
func (r *Reader) ClearPlanOverride() {
	r.state.clearPlanOverride()
}

// applyPlanOverride rewrites a resolved plan. It reports whether anything
// changed so callers only refresh the context when needed.
func (r *Reader) applyPlanOverride(plan *ReadPlan) bool {
	override, ok := r.state.planOverride()
	if !ok {
		return false
	}
	changed := false
	if override.MaxStreams > 0 && plan.Intent != ReadIntentInteractive {
		plan.MaxStreams = override.MaxStreams
		changed = true
	}
	if override.WindowChunks > 0 {
		plan.WindowChunks = override.WindowChunks
		changed = true
	}
	return changed
}

func (r *Reader) fileBudget() int      { return r.profile.MaxInFlight }
func (r *Reader) streamLimit() int     { return r.profile.MaxInFlight }
func (r *Reader) fileBudgetRoom() bool { return r.fileBudget() > 1 }

func (r *Reader) pendingUpload(path string) (vfstypes.PendingUpload, bool, error) {
	return r.host.PendingUpload(path)
}

// Read reads up to size bytes at offset, serving pending local changes
// from staging when present and otherwise reading remote chunks through
// the cache window machinery.
func (r *Reader) Read(ctx context.Context, path string, offset, size int64) (rc io.ReadCloser, err error) {
	defer func() { r.health.RecordResult(drive.HealthOpRead, err) }()
	path = CleanVirtualPath(path)
	started := util.Now()
	// One counter sample per read attempt, on every exit path. It measures
	// this call: the bytes the read path materialized before returning, and
	// the call's duration. The staging passthrough materializes nothing (it
	// opens a local file and hands the stream back), so it counts 0 bytes;
	// the read *event* for that path instead reports the bytes the consumer
	// eventually drains. Counters deliberately track remote materialization,
	// which is the part that costs bandwidth and time.
	var countedBytes int64
	defer func() { r.counters.RecordRead(countedBytes, util.Now().Sub(started), err) }()
	opID := r.observer.DebugNextOpID()
	activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
		OpID:      opID,
		Kind:      "vfs_read",
		Phase:     "resolve",
		Path:      path,
		Offset:    offset,
		Requested: size,
	})
	if pending, ok, err := r.pendingUpload(path); err == nil && ok {
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) {
			op.Phase = "staging_open"
			op.RemoteID = pending.FID
		})
		rc, err := util.OpenRead(pending.LocalPath, offset, size)
		if err != nil {
			r.observer.DebugFinishActive(activeID)
			r.observer.DebugRecordRead(opID, path, pending.FID, offset, size, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		return &debugReadCloser{ReadCloser: rc, finish: func(bytes int64, readErr error) {
			r.observer.DebugFinishActive(activeID)
			r.observer.DebugRecordRead(opID, path, pending.FID, offset, size, bytes, "staging", 0, 0, 0, started, nil, readErr)
		}}, nil
	}
	entry, err := r.resolve(ctx, path)
	if err != nil {
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, "", offset, size, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	if entry.IsDir {
		err := fmt.Errorf("vfs: %s is a directory", path)
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, entry.ID, offset, size, 0, "remote", 0, 0, 0, started, nil, err)
		return nil, err
	}
	r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) {
		op.Phase = "read_range"
		op.RemoteID = entry.ID
	})
	hitsBefore, missesBefore := r.observer.DebugCacheCounters()
	readCtx := drive.WithDebugOperation(ctx, drive.DebugOperation{OpID: opID, Step: "vfs_read", Name: path})
	readCtx = r.withFetchTimeouts(readCtx)
	readCtx, _ = withFetchTiming(readCtx)
	windowChunks := readWindowChunks(offset, size)
	cacheKey := r.host.ReadCacheKey(entry)
	r.state.beginForegroundRead(cacheKey)
	defer r.state.endForegroundRead(cacheKey)
	hint, hinted := AccessHintFromContext(ctx)
	readCtx = withReadSession(readCtx, hint.SessionID)
	decision := accessDecision{}
	// The read run must be known before the data is fetched, because that is
	// when chunks are admitted to the cache. A hinted read already has its
	// decision; an unhinted one only learns it after reading, so it starts from
	// the run the sequence state predicts and is corrected below.
	access := readcache.Access{Run: r.state.readRunHint(cacheKey, offset)}
	if hinted {
		decision = r.state.observeReadAccess(cacheKey, hint, offset, size)
		access = readcache.Access{Run: decision.run}
		if !decision.stale && !hint.Concurrent && decision.discontinuous {
			// A seek makes this session's old lookahead irrelevant. Cancel
			// only this session: another open handle reading the same file
			// keeps its own foreground and its own useful lookahead, and a
			// caller without a session identity is not guessed at.
			r.state.cancelScopesBySession(hint.SessionID, "seek")
		}
		if !decision.stale && !hint.Concurrent && decision.discontinuous &&
			offset%ChunkSize != 0 && size > 0 && size <= ChunkSize {
			// macOS FUSE commonly begins a seek with a small aligned-down
			// request. Fetch both touched chunks in one Range request so the
			// seek pays one CDN response-header delay instead of two.
			windowChunks = 2
		}
	}
	plan := r.resolvePlan(ctx, windowChunks, stageFor(hinted, hint, decision))
	windowChunks = plan.WindowChunks
	if hinted && !decision.stale && (hint.Concurrent || !readPrefetchEnabled(ctx) || plan.PrefetchDepth == 0) {
		r.state.scopes.cancelController(ctx, cacheKey)
	}
	loadKey := entry.ID
	readCtx = WithReadPlan(readCtx, plan)
	readCtx = WithGrantedStreams(readCtx, r.state.scheduler.GrantStreams(plan.MaxStreams, loadKey, r.streamLimit()))
	readCtx = applyIntentPriority(readCtx, plan)
	readCtx = WithCacheAccess(readCtx, access)
	if hinted && (decision.initial || decision.discontinuous) {
		RecordChunkDetail(r.observer, readCtx, entry, "read_access", offset/ChunkSize, offset%ChunkSize, size, 0, util.Now(), map[string]any{
			"session_id": hint.SessionID, "request_id": hint.RequestID,
			"initial": decision.initial, "discontinuous": decision.discontinuous,
			"window_chunks": windowChunks,
		}, nil)
	}
	firstWindowPrefetch := (!hinted && offset%ChunkSize != 0) || (hinted && offset == 0)
	if r.fileBudgetRoom() && readPrefetchEnabled(ctx) && plan.PrefetchDepth > 0 && !decision.stale && !hint.Concurrent && !decision.sequential && firstWindowPrefetch && windowChunks == 1 {
		// Warm the first lookahead window for a new file stream while the
		// foreground Range is loading. Non-zero offsets are treated as seeks
		// until contiguous access is observed, so random probes do not trigger
		// speculative network reads.
		r.prefetchWindow(readCtx, entry, offset/ChunkSize+1, prefetchWindowChunks(plan, windowChunks))
	}
	data, startChunk, endChunk, err := r.readRange(readCtx, entry, offset, size, windowChunks)
	hitsAfter, missesAfter := r.observer.DebugCacheCounters()
	if err != nil {
		if ctx.Err() != nil {
			r.state.cancelReadScope(ctx, cacheKey, "caller")
		}
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, entry.ID, offset, size, 0, "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, 0, started, ReadEventExtra(readCtx, windowChunks, err), err)
		return nil, err
	}
	if !hinted {
		decision = r.state.observeReadAccess(cacheKey, AccessHint{}, offset, int64(len(data)))
	} else if !r.state.readAccessCurrent(cacheKey, hint) {
		decision.stale = true
	}
	if actual := (readcache.Access{Run: decision.run}); actual != access {
		// The prediction for an unhinted read was superseded by a seek; the
		// lookahead scheduled below belongs to the run that was recorded.
		readCtx = WithCacheAccess(readCtx, actual)
	}
	if readPrefetchEnabled(ctx) && plan.PrefetchDepth > 0 && !decision.stale && !hint.Concurrent && (!hinted || decision.sequential || offset == 0) {
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) {
			op.Phase = "prefetch_schedule"
			op.Extra = map[string]any{
				"start_chunk":   startChunk,
				"end_chunk":     endChunk,
				"window_chunks": windowChunks,
				"sequential":    decision.sequential,
				"adaptive":      decision.adaptive,
			}
		})
		r.schedulePrefetch(readCtx, entry, endChunk, windowChunks, decision.sequential, plan)
	}
	var chunks int64
	if len(data) > 0 {
		chunks = endChunk - startChunk + 1
	}
	r.observer.DebugFinishActive(activeID)
	r.observer.DebugRecordRead(opID, path, entry.ID, offset, size, int64(len(data)), "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, chunks, started, ReadEventExtra(readCtx, windowChunks, nil), nil)
	countedBytes = int64(len(data))
	return io.NopCloser(bytes.NewReader(data)), nil
}

// CleanVirtualPath normalizes qrypt virtual paths to absolute slash paths.
func CleanVirtualPath(path string) string { return vfstypes.CleanVirtualPath(path) }

func (r *Reader) readRange(ctx context.Context, entry drive.Entry, offset, size int64, windowChunks int) ([]byte, int64, int64, error) {
	if offset < 0 || size < 0 {
		return nil, 0, 0, fmt.Errorf("vfs: read offset and size must be non-negative")
	}
	startChunk := offset / ChunkSize
	endChunk := startChunk
	if entry.Size > 0 && offset >= entry.Size {
		return nil, startChunk, endChunk, nil
	}
	var out bytes.Buffer
	if size > 0 && size <= ChunkSize {
		out.Grow(int(size))
	}
	pos := offset
	end, endKnown := readEnd(offset, size, entry.Size)
	for !endKnown || pos < end {
		chunkIndex := pos / ChunkSize
		chunkStart := chunkIndex * ChunkSize
		start := pos - chunkStart
		want := int64(ChunkSize) - start
		if endKnown && end-pos < want {
			want = end - pos
		}
		chunk, err := r.readChunkRange(ctx, entry, chunkIndex, start, want, windowChunks)
		if err != nil {
			return nil, startChunk, endChunk, err
		}
		if len(chunk) == 0 {
			break
		}
		deliverStarted := util.Now()
		out.Write(chunk)
		fetchTimingFromContext(ctx).recordDeliver(util.Now().Sub(deliverStarted))
		endChunk = chunkIndex
		pos += int64(len(chunk))
		if int64(len(chunk)) < want || (endKnown && pos >= end) {
			break
		}
	}
	return out.Bytes(), startChunk, endChunk, nil
}

func readEnd(offset, size, entrySize int64) (int64, bool) {
	if size > 0 {
		end := offset + size
		if entrySize > 0 && end > entrySize {
			end = entrySize
		}
		return end, true
	}
	if entrySize > 0 {
		return entrySize, true
	}
	return 0, false
}

// --- runtime wiring ---

// readRuntime abstracts the read domain's state access so chunk logic is
// testable with a fake.
type readRuntime interface {
	CacheKey(entry drive.Entry) string
	AddCacheHit()
	HotChunk(cacheKey string, index int64) ([]byte, bool)
	PutHotChunk(cacheKey string, index int64, data []byte)
	ShouldPromoteCachedRange(cacheKey string, index int64) bool
	RecordCachedRangeHit(cacheKey string, index, requestSize int64)
	ChunkAvailable(cacheKey string, index int64) bool
	GetChunkWithRange(cacheKey string, index, start, size int64, access readcache.Access) ([]byte, []byte, bool, error)
	GetChunkRange(cacheKey string, index, start, size int64, access readcache.Access) ([]byte, bool, error)
	WaitWindow(ctx context.Context, cacheKey string, index int64) ([]byte, bool, error)
	LoadWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error)
}

func (r *Reader) newRuntime() readRuntime { return &stateRuntime{reader: r} }

// stateRuntime adapts Reader/State to readRuntime.
type stateRuntime struct {
	reader *Reader
}

func (rt *stateRuntime) CacheKey(entry drive.Entry) string {
	return rt.reader.host.ReadCacheKey(entry)
}

func (rt *stateRuntime) AddCacheHit() {
	if rt.reader.state.cache != nil {
		rt.reader.state.cache.AddHit()
	}
}

func (rt *stateRuntime) HotChunk(cacheKey string, index int64) ([]byte, bool) {
	return rt.reader.hotChunk(cacheKey, index)
}

func (rt *stateRuntime) PutHotChunk(cacheKey string, index int64, data []byte) {
	rt.reader.putHotChunk(cacheKey, index, data)
}

func (rt *stateRuntime) ShouldPromoteCachedRange(cacheKey string, index int64) bool {
	return rt.reader.state.shouldPromoteCachedRange(cacheKey, index)
}

func (rt *stateRuntime) RecordCachedRangeHit(cacheKey string, index, requestSize int64) {
	rt.reader.state.recordCachedRangeHit(cacheKey, index, requestSize)
}

func (rt *stateRuntime) ChunkAvailable(cacheKey string, index int64) bool {
	return rt.reader.readChunkAvailable(cacheKey, index)
}

func (rt *stateRuntime) GetChunkWithRange(cacheKey string, index, start, size int64, access readcache.Access) ([]byte, []byte, bool, error) {
	if rt.reader.state.cache == nil {
		return nil, nil, false, nil
	}
	return rt.reader.state.cache.GetChunkWithRange(cacheKey, index, start, size, access)
}

func (rt *stateRuntime) GetChunkRange(cacheKey string, index, start, size int64, access readcache.Access) ([]byte, bool, error) {
	if rt.reader.state.cache == nil {
		return nil, false, nil
	}
	return rt.reader.state.cache.GetChunkRange(cacheKey, index, start, size, access)
}

func (rt *stateRuntime) WaitWindow(ctx context.Context, cacheKey string, index int64) ([]byte, bool, error) {
	return rt.reader.waitWindow(ctx, cacheKey, index)
}

func (rt *stateRuntime) LoadWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error) {
	return rt.reader.loadWindow(ctx, entry, startIndex, count)
}

// readWindowBackend abstracts the driver read + cache store for
// fetchChunkWindow, testable with a fake.
type readWindowBackend interface {
	CacheKey(entry drive.Entry) string
	StoreChunk(ctx context.Context, cacheKey string, entry drive.Entry, index int64, chunk []byte)
}

func (r *Reader) newBackend() readWindowBackend {
	return &readerBackend{reader: r}
}

type readerBackend struct {
	reader *Reader
}

func (b *readerBackend) CacheKey(entry drive.Entry) string {
	return b.reader.host.ReadCacheKey(entry)
}

func (b *readerBackend) StoreChunk(ctx context.Context, cacheKey string, entry drive.Entry, index int64, chunk []byte) {
	b.reader.policy.Admit(ctx, entry, cacheKey, index, chunk)
}

func (r *Reader) readChunkRange(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int) ([]byte, error) {
	return r.readChunkRangeWithRuntime(ctx, entry, index, start, size, windowChunks, r.newRuntime())
}

func (r *Reader) readChunkRangeWithRuntime(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int, runtime readRuntime) ([]byte, error) {
	cacheKey := runtime.CacheKey(entry)
	access := cacheAccess(ctx)
	if data, ok, err := r.lookupCachedChunk(ctx, entry, cacheKey, index, start, size, access, runtime, "", "", true); err != nil {
		return nil, err
	} else if ok {
		return data, nil
	}
	waitStarted := util.Now()
	if data, ok, err := runtime.WaitWindow(ctx, cacheKey, index); err != nil {
		RecordChunkDetail(r.observer, ctx, entry, "wait_window", index, start, size, 0, waitStarted, nil, err)
		return nil, err
	} else if ok {
		if data != nil {
			chunk := sliceChunkRange(data, start, size)
			RecordChunkDetail(r.observer, ctx, entry, "wait_window", index, start, size, int64(len(chunk)), waitStarted, nil, nil)
			return chunk, nil
		}
		if data, ok, err := r.lookupCachedChunk(ctx, entry, cacheKey, index, start, size, access, runtime, "wait_window_", "wait_window_", false); err != nil {
			return nil, err
		} else if ok {
			return data, nil
		}
	}
	var data []byte
	var err error
	loadStarted := util.Now()
	data, err = runtime.LoadWindow(ctx, entry, index, windowChunks)
	if err != nil {
		RecordChunkDetail(r.observer, ctx, entry, "cache_miss_load", index, start, size, 0, loadStarted, nil, err)
		return nil, err
	}
	chunk := sliceChunkRange(data, start, size)
	RecordChunkDetail(r.observer, ctx, entry, "cache_miss_load", index, start, size, int64(len(chunk)), loadStarted, WindowExtra(windowChunks), nil)
	return chunk, nil
}

// lookupCachedChunk is the single cache lookup path for a chunk range. The
// phase and source prefixes keep the existing diagnostics for a direct miss
// and for a lookup after WaitWindow.
func (r *Reader) lookupCachedChunk(ctx context.Context, entry drive.Entry, cacheKey string, index, start, size int64, access readcache.Access, runtime readRuntime, phasePrefix, sourcePrefix string, checkHot bool) ([]byte, bool, error) {
	if cacheKey == "" {
		return nil, false, nil
	}
	if checkHot {
		hotStarted := util.Now()
		if hot, ok := runtime.HotChunk(cacheKey, index); ok {
			runtime.AddCacheHit()
			data := sliceChunkRange(hot, start, size)
			RecordChunkDetail(r.observer, ctx, entry, phasePrefix+"cache_hot_hit", index, start, size, int64(len(data)), hotStarted, CacheLookupExtra("hot", hotStarted, nil), nil)
			return data, true, nil
		}
	}
	if shouldPromoteCachedRange(size) && runtime.ShouldPromoteCachedRange(cacheKey, index) {
		started := util.Now()
		if cached, chunk, ok, err := runtime.GetChunkWithRange(cacheKey, index, start, size, access); err != nil {
			RecordChunkDetail(r.observer, ctx, entry, phasePrefix+"cache_range_promote", index, start, size, 0, started, CacheLookupExtra(sourcePrefix+"range_promote", started, nil), err)
			return nil, false, err
		} else if ok {
			if len(chunk) > 0 {
				runtime.PutHotChunk(cacheKey, index, chunk)
			}
			RecordChunkDetail(r.observer, ctx, entry, phasePrefix+"cache_range_promote", index, start, size, int64(len(cached)), started, CacheLookupExtra(sourcePrefix+"range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
			return cached, true, nil
		}
	}
	started := util.Now()
	if cached, ok, err := runtime.GetChunkRange(cacheKey, index, start, size, access); err != nil {
		RecordChunkDetail(r.observer, ctx, entry, phasePrefix+"cache_range_hit", index, start, size, 0, started, CacheLookupExtra(sourcePrefix+"range", started, nil), err)
		return nil, false, err
	} else if ok {
		runtime.RecordCachedRangeHit(cacheKey, index, size)
		RecordChunkDetail(r.observer, ctx, entry, phasePrefix+"cache_range_hit", index, start, size, int64(len(cached)), started, CacheLookupExtra(sourcePrefix+"range", started, nil), nil)
		return cached, true, nil
	}
	return nil, false, nil
}

func sliceChunkRange(data []byte, start, size int64) []byte {
	if start < 0 || size < 0 || start >= int64(len(data)) {
		return nil
	}
	stop := int64(len(data))
	if size > 0 && start+size < stop {
		stop = start + size
	}
	return data[start:stop]
}

func readWindowChunks(offset, requestSize int64) int {
	if requestSize > 0 && requestSize <= ChunkSize {
		// A large unaligned read that crosses a chunk boundary is cheaper as
		// one aligned two-chunk Range request than two independent requests.
		// Keep small boundary reads on the one-chunk path to avoid fetching a
		// disproportionate amount of data for metadata/thumbnail probes.
		if offset >= 0 && offset%ChunkSize != 0 &&
			offset%ChunkSize+requestSize > ChunkSize && requestSize >= ChunkSize/2 {
			return 2
		}
		return 1
	}
	return PrefetchChunks
}

func ReadAllLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("vfs: read limit must be non-negative")
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("vfs: driver returned more data than requested: limit=%d read=%d", limit, len(data))
	}
	return data, nil
}

// --- window machinery ---

func (r *Reader) loadWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error) {
	if count <= 0 {
		count = 1
	}
	cacheKey := r.host.ReadCacheKey(entry)
	endIndex := startIndex + int64(count) - 1
	if entry.Size > 0 {
		lastIndex := (entry.Size - 1) / ChunkSize
		if endIndex > lastIndex {
			endIndex = lastIndex
		}
	}
	plan, _ := readPlanFromContext(ctx)
	streams := grantedStreams(ctx, plan.MaxStreams)
	if streams < 1 {
		streams = 1
	}
	if streams > 1 && r.state.scheduler.IsNonParallel(entry.ID) {
		streams = 1
	}
	if cacheKey != "" {
		for endIndex > startIndex && r.readChunkAvailableFor(cacheKey, endIndex, streams) {
			endIndex--
		}
	}
	key := WindowShapeKey(WindowKey(LoadKey(r.host, entry), startIndex, endIndex), streams)
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, streams: streams, done: make(chan struct{})}
	if entry.Size > 0 {
		// Known-size windows publish completed blocks: initialize the per-block
		// channels before the load is registered so joiners can wait on them.
		// Bulk windows use the same part scheduler, so downloads and playback
		// share one execution path.
		load.progressive = true
		load.chunks = make([]windowChunk, int(endIndex-startIndex+1))
		for index := range load.chunks {
			load.chunks[index].ready = make(chan struct{})
		}
		load.data = make(map[int64][]byte, len(load.chunks))
	}
	for attempt := 0; attempt <= fetchTimeoutAttempts; attempt++ {
		existing, joined := r.state.beginWindowLoad(key, load)
		if !joined {
			break
		}
		detach, ok := r.state.attachWindow(existing)
		if !ok {
			continue
		}
		defer detach()
		started := util.Now()
		activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
			Kind:        "vfs_wait",
			Phase:       "wait_window_load",
			Path:        DebugOperationName(ctx),
			RemoteID:    entry.ID,
			ChunkIndex:  startIndex,
			WindowStart: startIndex,
			WindowEnd:   endIndex,
			WaitFor:     key,
		})
		RecordChunkDetail(r.observer, ctx, entry, "wait_window_load_begin", startIndex, 0, ChunkSize, 0, started, windowWaitExtra(existing), nil)
		ready, waitErr := existing.waitTarget(ctx, startIndex)
		switch {
		case waitErr == nil && !ready:
			// The joined window finished without this block; retry as the
			// owner of a window that covers exactly what this read needs.
			detach()
			r.observer.DebugFinishActive(activeID)
			continue
		case waitErr == nil:
			if len(existing.chunks) != 0 {
				if data, _ := existing.result(startIndex); data != nil {
					fetchTimingFromContext(ctx).recordWait(util.Now().Sub(started))
					extra := windowWaitExtra(existing)
					extra["wait_us"] = util.Now().Sub(started).Microseconds()
					RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, int64(len(data)), started, extra, nil)
					r.observer.DebugFinishActive(activeID)
					return data, nil
				}
			}
			waitFinished := util.Now()
			extra := ExtraWithWindow(existing.extra)
			extra["wait_us"] = waitFinished.Sub(started).Microseconds()
			if existing.err != nil {
				// A timed-out or abandoned prefetch is a silent cancellation
				// for the prefetch itself, but a foreground read still needs
				// its data. Take the window over and fetch it here instead of
				// surfacing a background failure as the caller's error; the
				// abandoned load has been unregistered by now, so the retry
				// registers this reader's own window.
				if existing.prefetch && attempt < fetchTimeoutAttempts && ctx.Err() == nil &&
					(isReadFetchTimeout(existing.err) || errors.Is(existing.err, context.Canceled) || errors.Is(existing.err, drive.ErrReadAdmissionBusy)) {
					if isReadFetchTimeout(existing.err) {
						extra["cancel_reason"] = "timeout"
					} else {
						extra["cancel_reason"] = "canceled"
					}
					RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, 0, started, extra, existing.err)
					r.observer.DebugFinishActive(activeID)
					continue
				}
				if isReadFetchTimeout(existing.err) {
					extra["cancel_reason"] = "timeout"
				}
				RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, 0, started, extra, existing.err)
				r.observer.DebugFinishActive(activeID)
				return nil, existing.err
			}
			fetchTimingFromContext(ctx).recordWait(waitFinished.Sub(started))
			RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, int64(len(existing.data[startIndex])), started, extra, nil)
			r.observer.DebugFinishActive(activeID)
			return existing.data[startIndex], nil
		default:
			RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, 0, started, map[string]any{
				"window_start": startIndex,
				"window_end":   endIndex,
				"wait_us":      util.Now().Sub(started).Microseconds(),
			}, ctx.Err())
			r.observer.DebugFinishActive(activeID)
			return nil, ctx.Err()
		}
	}

	started := util.Now()
	r.startWindowFetch(ctx, entry, startIndex, endIndex, load)
	// The fetch runs in its own goroutine, so the block this read waits for is
	// served as soon as the drive publishes it instead of after the window tail.
	detach, attached := r.state.attachWindow(load)
	if attached {
		defer detach()
		ready, waitErr := load.waitTarget(ctx, startIndex)
		switch {
		case waitErr != nil:
			if data, ok := load.published(startIndex); ok {
				fetchTimingFromContext(ctx).recordWait(util.Now().Sub(started))
				return data, nil
			}
			// The caller went away before its block was published. Cancel the
			// window fetch so it stops holding a provider slot, unless another
			// reader has already joined the window.
			detach()
			detach = nil
			if r.state.abandonWindow(load, 0, true) && load.cancel != nil {
				// Cancel the fetch and wait for it to unwind so the read does
				// not return while the window still holds a provider slot.
				load.cancel()
				<-load.done
			}
			return nil, waitErr
		case ready:
			if data, ok := load.published(startIndex); ok {
				fetchTimingFromContext(ctx).recordWait(util.Now().Sub(started))
				return data, nil
			}
			// The window finished without publishing this block; fall through to
			// the fetch outcome so the caller sees the error instead of hanging.
			<-load.done
		}
	} else {
		<-load.done
	}
	if data, ok := load.published(startIndex); ok {
		fetchTimingFromContext(ctx).recordWait(util.Now().Sub(started))
		return data, nil
	}
	if load.err != nil {
		return nil, load.err
	}
	if data, ok := load.data[startIndex]; ok && len(data) > 0 {
		return data, nil
	}
	return nil, fmt.Errorf("vfs: window load finished without block %d", startIndex)
}

// startWindowFetch runs one foreground window fetch in its own goroutine and
// ties its lifetime to the stream scope: a seek, session release or file close
// abandons the window and cancels the fetch, while the reading goroutine waits
// only for the block it needs.
func (r *Reader) startWindowFetch(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, load *windowLoad) {
	cacheKey := r.host.ReadCacheKey(entry)
	scope, releaseScope := r.state.scopeFor(ctx, cacheKey)
	fetchCtx, cancelFetch := context.WithCancelCause(context.WithoutCancel(ctx))
	load.cancel = func() { cancelFetch(drive.ReadCallerCanceled) }
	stopScope := context.AfterFunc(scope.ctx, func() {
		if r.state.abandonWindow(load, 0, true) {
			if reason := scope.cancelReason(); reason == "seek" || reason == "session_release" {
				cancelFetch(drive.ReadSuperseded)
			} else {
				cancelFetch(drive.ReadCallerCanceled)
			}
		}
	})
	go func() {
		defer releaseScope()
		defer stopScope()
		defer cancelFetch(nil)
		r.runWindowFetch(fetchCtx, entry, startIndex, endIndex, load)
	}()
}

// runWindowFetch executes one foreground window, publishing each block as the
// drive layer completes it, then closes done and unregisters the load.
func (r *Reader) runWindowFetch(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, load *windowLoad) {
	cacheKey := r.host.ReadCacheKey(entry)
	plan, _ := readPlanFromContext(ctx)
	streams := grantedStreams(ctx, plan.MaxStreams)
	started := util.Now()
	activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
		Kind:        "vfs_window_load",
		Phase:       "acquire_slot",
		Path:        DebugOperationName(ctx),
		RemoteID:    entry.ID,
		Offset:      startIndex * ChunkSize,
		Requested:   (endIndex - startIndex + 1) * ChunkSize,
		ChunkIndex:  startIndex,
		WindowStart: startIndex,
		WindowEnd:   endIndex,
		ReadIntent:  plan.Intent.String(),
		ReadStreams: streams,
	})
	r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
	if load.progressive {
		load.extra, load.err = r.streamChunkWindow(ctx, entry, startIndex, endIndex, load)
	} else {
		load.data, load.extra, load.err = r.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	}
	fetchFinished := util.Now()
	fetchTimingFromContext(ctx).recordFetch(fetchFinished.Sub(started))
	r.observer.DebugFinishActive(activeID)
	recordWindowFetchTimeouts(r.state, false, cacheKey, startIndex, endIndex, load.extra)
	fetchExtra := ExtraWithWindow(load.extra)
	// Attach the plan slice that explains this window's shape; the full plan is
	// on the summary read event.
	for key, value := range readPlanWindowExtra(ctx) {
		if _, exists := fetchExtra[key]; !exists {
			fetchExtra[key] = value
		}
	}
	fetchExtra["window_chunks"] = int(endIndex - startIndex + 1)
	fetchExtra["fetch_us"] = fetchFinished.Sub(started).Microseconds()
	if isReadFetchTimeout(load.err) {
		fetchExtra["cancel_reason"] = "timeout"
	}
	RecordChunkDetail(r.observer, ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, fetchExtra, load.err)
	if load.progressive {
		// Wake any waiter of a block this fetch did not publish; result() then
		// reports the window error instead of blocking forever.
		load.finishChunks()
	}
	close(load.done)
	r.state.endWindowLoad(load)
}

// windowFetch is the prepared shape of one foreground window fetch: the plan
// resolved for this read, the stream/split choice, and the trace that carries
// the range diagnostics. The buffered and progressive fetch paths share it so
// the adaptive split sees the same inputs either way.
type windowFetch struct {
	ctx         context.Context
	entry       drive.Entry
	plan        ReadPlan
	windowBytes int64
	streams     int
	partBytes   int64
	adaptive    bool
	started     time.Time
	trace       *drive.ReadTrace
}

func (r *Reader) prepareWindowFetch(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) windowFetch {
	ctx = r.withFetchTimeouts(ctx)
	plan, _ := readPlanFromContext(ctx)
	ctx = r.withReadFaults(ctx)
	trace := r.newReadTrace(ctx, entry)
	ctx = drive.WithReadTrace(ctx, trace)
	windowBytes := (endIndex - startIndex + 1) * ChunkSize
	streams := grantedStreams(ctx, plan.MaxStreams)
	if streams < 1 {
		streams = 1
	}
	prefetch := isPrefetchRead(ctx)
	adaptive := foregroundSplitEnabled() && !prefetch && plan.streaming() && windowBytes >= ChunkSize
	partBytes := int64(0)
	if adaptive {
		if parts := r.foregroundSplitRequested(entry.ID); parts > 1 {
			streams = parts
			if limit := r.streamLimit(); limit > 0 && streams > limit {
				streams = limit
			}
			if streams > 1 {
				// Split windows use an even layout: each worker takes an equal
				// share of the window. A fixed small part would multiply Range
				// requests without adding parallelism, and the even split keeps
				// the Range count equal to the worker count.
				partBytes = (windowBytes + int64(streams) - 1) / int64(streams)
			}
		}
	} else if streams > 1 {
		// Streaming and bulk windows take their part layout from the driver's
		// split policy, translated here so the source executes an explicit
		// shape and never interprets policy itself.
		streams, partBytes = drive.WindowPartLayout(windowBytes, streams, plan.Split)
		if streams > 1 && partBytes <= 0 {
			partBytes = (windowBytes + int64(streams) - 1) / int64(streams)
		}
	}
	return windowFetch{ctx: ctx, entry: entry, plan: plan, windowBytes: windowBytes, streams: streams, partBytes: partBytes, adaptive: adaptive, started: time.Now(), trace: trace}
}

func (w windowFetch) rangePlan(startIndex int64) drive.RangePlan {
	// The read layer owns the window's part layout; the drive source only
	// executes the ranges it is given.
	return drive.RangePlan{
		Offset:      startIndex * ChunkSize,
		Size:        w.windowBytes,
		Streams:     w.streams,
		Workers:     w.streams,
		Part:        w.partBytes,
		Timeouts:    fetchTimeoutsFromContext(w.ctx),
		Priority:    priority(w.ctx),
		Speculative: isPrefetchRead(w.ctx),
	}
}

// observeWindowFetch feeds the window's transfer rate into the adaptive
// foreground split and attaches its shape to the window's diagnostics.
func (r *Reader) observeWindowFetch(w windowFetch, snapshot map[string]any, delivered int64, elapsed time.Duration) {
	if !w.adaptive {
		return
	}
	if us, ok := snapshot["body_us"].(int64); ok && us > 0 {
		// Judge the provider's transfer rate, not its open latency: a fast
		// link with a slow request open must not arm the split.
		elapsed = time.Duration(us) * time.Microsecond
	}
	if w.streams > 1 {
		snapshot["fg_split_streams"] = w.streams
		snapshot["fg_split_part_bytes"] = w.partBytes
	}
	r.observeForegroundRate(w.entry.ID, delivered, elapsed)
	snapshot["fg_split_mode"] = r.foregroundSplitting(w.entry.ID)
	if elapsed > 0 {
		snapshot["fg_split_rate_kb_s"] = int64(float64(delivered) / elapsed.Seconds() / 1024)
	}
}

func (r *Reader) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	w := r.prepareWindowFetch(ctx, entry, startIndex, endIndex)
	data, err := r.source.Fetch(w.ctx, entry, w.rangePlan(startIndex))
	if err != nil {
		return nil, w.trace.Snapshot(), err
	}
	snapshot := w.trace.Snapshot()
	r.observeWindowFetch(w, snapshot, int64(len(data)), time.Since(w.started))
	return storeWindowChunks(w.ctx, r.newBackend(), entry, startIndex, endIndex, data), snapshot, nil
}

// streamChunkWindow is fetchChunkWindow with progressive publication: every
// block the drive layer completes is stored and published while the rest of
// the window is still in flight, so a reader waiting for the head block does
// not pay for the window's tail.
func (r *Reader) streamChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, load *windowLoad) (map[string]any, error) {
	w := r.prepareWindowFetch(ctx, entry, startIndex, endIndex)
	backend := r.newBackend()
	delivered := int64(0)
	var err error
	if w.streams > 1 && w.partBytes > 0 && w.rangePlan(startIndex).Offset%ChunkSize == 0 {
		// A split foreground window is scheduled by the read layer: it owns the
		// worker count and part layout instead of delegating the whole window.
		delivered, err = r.streamWindowParts(w.ctx, entry, w, startIndex, load, backend)
	} else {
		err = r.source.FetchStream(w.ctx, entry, w.rangePlan(startIndex), func(offset int64, block []byte) {
			index := offset / ChunkSize
			backend.StoreChunk(w.ctx, backend.CacheKey(entry), entry, index, block)
			load.data[index] = block
			load.publish(index, block)
			delivered += int64(len(block))
		})
	}
	snapshot := w.trace.Snapshot()
	if err == nil {
		r.observeWindowFetch(w, snapshot, delivered, time.Since(w.started))
	}
	return snapshot, err
}

const fetchTimeoutAttempts = 2

func storeWindowChunks(ctx context.Context, backend readWindowBackend, entry drive.Entry, startIndex, endIndex int64, data []byte) map[int64][]byte {
	chunks := make(map[int64][]byte, len(data)/ChunkSize+1)
	remaining := data
	for index := startIndex; len(remaining) > 0 && index <= endIndex; index++ {
		chunkSize := ChunkSize
		if len(remaining) < chunkSize {
			chunkSize = len(remaining)
		}
		// Slice the single window buffer instead of per-chunk make+copy: the
		// cache retains the slice, so all chunks of one window share one
		// backing array (same total memory, zero copy cost).
		chunk := remaining[:chunkSize]
		chunks[index] = chunk
		backend.StoreChunk(ctx, backend.CacheKey(entry), entry, index, chunk)
		remaining = remaining[chunkSize:]
	}
	return chunks
}

func (r *Reader) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
	if fid == "" {
		return nil, false, nil
	}
	plan, _ := readPlanFromContext(ctx)
	streams := grantedStreams(ctx, plan.MaxStreams)
	if streams < 1 {
		streams = 1
	}
	// A first read or a seek delivers a deliberately small window. Waiting for
	// an oversized buffered window would charge it for unrelated lookahead.
	// Progressive windows can supply an already-ready block or their head
	// block without waiting for the tail; far-away unpublished blocks remain
	// misses. Sequential and bulk readers may wait for any covered target.
	maxChunks := int64(0)
	if plan.Intent != ReadIntentBulk && plan.Stage != StageSequential && plan.WindowChunks > 0 {
		maxChunks = int64(plan.WindowChunks)
	}
	load := r.state.findWindow(fid, index, streams, maxChunks, readSession(ctx))
	if load == nil {
		return nil, false, nil
	}
	detach, ok := r.state.attachWindow(load)
	if !ok {
		return nil, false, nil
	}
	defer detach()
	activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
		Kind:        "vfs_wait",
		Phase:       "wait_window",
		Path:        DebugOperationName(ctx),
		RemoteID:    fid,
		ChunkIndex:  index,
		WindowStart: load.start,
		WindowEnd:   load.end,
		WaitFor:     WindowShapeKey(WindowKey(load.fid, load.start, load.end), load.streams),
	})
	started := util.Now()
	timing := fetchTimingFromContext(ctx)
	defer func() { timing.recordWait(util.Now().Sub(started)) }()
	defer r.observer.DebugFinishActive(activeID)
	RecordChunkDetail(r.observer, ctx, drive.Entry{ID: fid}, "wait_window_begin", index, 0, ChunkSize, 0, started, windowWaitExtra(load), nil)
	ready, waitErr := load.waitTarget(ctx, index)
	switch {
	case waitErr == nil && !ready:
		// The speculative window finished without publishing this block; that
		// is a miss, so the caller fetches it in its own window.
		return nil, false, nil
	case waitErr == nil:
		data, err := load.result(index)
		extra := windowWaitExtra(load)
		extra["wait_us"] = util.Now().Sub(started).Microseconds()
		RecordChunkDetail(r.observer, ctx, drive.Entry{ID: fid}, "wait_window_done", index, 0, ChunkSize, int64(len(data)), started, extra, err)
		if err != nil {
			// A timed-out prefetch is a miss for a foreground reader: let the
			// caller load the window itself instead of surfacing a background
			// failure as its own.
			if load.prefetch && (isReadFetchTimeout(err) || errors.Is(err, drive.ErrReadAdmissionBusy)) {
				RecordChunkDetail(r.observer, ctx, drive.Entry{ID: fid}, "wait_window", index, 0, ChunkSize, 0, started, map[string]any{
					"wait_us":       util.Now().Sub(started).Microseconds(),
					"cancel_reason": "timeout",
				}, err)
				return nil, false, nil
			}
			if errors.Is(err, context.Canceled) {
				return nil, false, nil
			}
			return nil, true, err
		}
		return data, true, nil
	default:
		return nil, true, waitErr
	}
}

func windowWaitExtra(load *windowLoad) map[string]any {
	extra := map[string]any{
		"window_chunks": load.end - load.start + 1, "window_streams": load.streams,
	}
	if load.trace != nil {
		for key, value := range load.trace.Snapshot() {
			extra["window_"+key] = value
		}
	}
	return extra
}

func (r *Reader) readChunkAvailable(cacheKey string, index int64) bool {
	return r.readChunkAvailableFor(cacheKey, index, 0)
}

// readChunkAvailableFor reports whether index is already covered by a hot
// chunk, the durable cache, or an in-flight window of the same fetch shape.
// streams <= 0 accepts any shape.
func (r *Reader) readChunkAvailableFor(cacheKey string, index int64, streams int) bool {
	if _, ok := r.hotChunk(cacheKey, index); ok {
		return true
	}
	if r.state.cache != nil {
		if ok, err := r.state.cache.HasChunk(cacheKey, index); err != nil || ok {
			return true
		}
	}
	return r.state.findWindow(cacheKey, index, streams, 0, 0) != nil
}

func (r *Reader) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return r.state.hotChunk(cacheKey, index)
}

func (r *Reader) putHotChunk(cacheKey string, index int64, data []byte) {
	r.state.putHotChunk(cacheKey, index, data)
}

// --- prefetch ---

func envFlag(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// foregroundSplitEnabled reports whether the adaptive foreground-window split
// is active. It is on by default: once a foreground window's measured rate
// shows a per-connection throttle, later windows are fetched as several
// parallel Range requests instead of one, so the provider cannot gate the whole
// read on a single connection, and the mode falls back to one stream once the
// rate recovers. QRYPT_FG_SPLIT=0 disables it for one run while triaging.
func foregroundSplitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("QRYPT_FG_SPLIT"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

const foregroundSplitStreams = 4

// Adaptive thresholds: a foreground window is split only after a measured
// window rate falls below fgSplitEnterBytesPerSec, and the split is dropped
// again once a window reaches fgSplitExitBytesPerSec. The hysteresis keeps the
// mode stable instead of flapping on one noisy sample. They are variables so a
// test can force either decision without waiting on real network timing.
var (
	fgSplitEnterBytesPerSec int64 = 350 << 10
	fgSplitExitBytesPerSec  int64 = 900 << 10
	// fgSplitSlowWindows is how many consecutive slow windows must be observed
	// before the adaptive mode starts splitting. One is enough: playback's first
	// sequential window is megabytes wide, so waiting for a second slow sample
	// would stall the player for that whole window. A false positive on a fast
	// link is bounded because one split window above the exit rate reverts it.
	fgSplitSlowWindows = 1
	// fgSplitThrottleMemory keeps the split armed for objects that have no rate
	// sample yet after any object recently measured a slow window: opening the
	// next file during the same throttled session should not pay the cold-start
	// single-stream window again. It expires on its own.
	fgSplitThrottleMemory = 5 * time.Minute
)

// fgSplitState is one object's adaptive split decision and last measured rate.
type fgSplitState struct {
	rate       int64
	slowStreak int
	splitting  bool
}

// observeForegroundRate records the measured throughput of one foreground
// window and flips the adaptive split mode with hysteresis.
func (r *Reader) observeForegroundRate(objectID string, bytes int64, elapsed time.Duration) {
	if objectID == "" || bytes <= 0 || elapsed <= 0 {
		return
	}
	rate := int64(float64(bytes) / elapsed.Seconds())
	r.fgMu.Lock()
	defer r.fgMu.Unlock()
	state := r.fgSplit[objectID]
	if state == nil {
		state = &fgSplitState{}
		r.fgSplit[objectID] = state
	}
	state.rate = rate
	if rate < fgSplitEnterBytesPerSec {
		r.fgLastSlow = time.Now()
	}
	if state.splitting {
		if rate >= fgSplitExitBytesPerSec {
			state.splitting = false
			state.slowStreak = 0
		}
		return
	}
	if rate >= fgSplitEnterBytesPerSec {
		state.slowStreak = 0
		return
	}
	state.slowStreak++
	if state.slowStreak >= fgSplitSlowWindows {
		state.splitting = true
	}
}

// foregroundSplitRequested reports how many streams the adaptive mode wants for
// this object's foreground windows (0 = keep one stream).
func (r *Reader) foregroundSplitRequested(objectID string) int {
	r.fgMu.Lock()
	defer r.fgMu.Unlock()
	state := r.fgSplit[objectID]
	if state != nil && state.splitting {
		return foregroundSplitStreams
	}
	if (state == nil || state.rate == 0) && !r.fgLastSlow.IsZero() && time.Since(r.fgLastSlow) < fgSplitThrottleMemory {
		// No sample for this object yet and the provider was recently slow for
		// another one: hedge the first window instead of paying a cold-start
		// single-stream window during the same throttled session.
		return foregroundSplitStreams
	}
	return 0
}

// foregroundSplitting reports whether speculative lookahead should stand down
// because the foreground is using the object's connections for its own parts.
func (r *Reader) foregroundSplitting(objectID string) bool {
	return r.foregroundSplitRequested(objectID) > 0
}

// prefetchWindowChunks is the speculative window size for a planned read: the
// driver's stage suggestion when it made one, otherwise the foreground window.
func prefetchWindowChunks(plan ReadPlan, windowChunks int) int {
	if plan.PrefetchChunks > 0 {
		return plan.PrefetchChunks
	}
	return windowChunks
}

func (r *Reader) prefetchWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) *windowLoad {
	return r.startPrefetchWindow(ctx, entry, startIndex, count, false)
}

func (r *Reader) startPrefetchWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int, bounded bool) *windowLoad {
	if startIndex < 0 || count <= 0 {
		return nil
	}
	if entry.Size > 0 && startIndex*ChunkSize >= entry.Size {
		return nil
	}
	cacheKey := r.host.ReadCacheKey(entry)
	if cacheKey == "" {
		return nil
	}
	r.state.prefetchMu.Lock()
	defer r.state.prefetchMu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	stats := r.state.scheduler.Stats()
	if stats.PrefetchInUse >= stats.PrefetchCapacity {
		return nil
	}
	ctx = r.withFetchTimeouts(ctx)
	plannedEndIndex := startIndex + int64(count) - 1
	for startIndex <= plannedEndIndex {
		if entry.Size > 0 && startIndex*ChunkSize >= entry.Size {
			return nil
		}
		if r.readChunkAvailableFor(cacheKey, startIndex, 1) {
			startIndex++
			continue
		}
		break
	}
	if startIndex > plannedEndIndex {
		return nil
	}
	maxEndIndex := plannedEndIndex
	if !bounded {
		// One-shot and ReadStream callers retain their merged window size;
		// the scheduler's bounded path already derives its span from the plan.
		maxEndIndex = startIndex + int64(count) - 1
	}
	endIndex := startIndex
	for endIndex <= maxEndIndex {
		if entry.Size > 0 && endIndex*ChunkSize >= entry.Size {
			break
		}
		if endIndex > startIndex && r.readChunkAvailableFor(cacheKey, endIndex, 1) {
			break
		}
		endIndex++
	}
	endIndex--
	if endIndex < startIndex {
		return nil
	}
	key := WindowKey(cacheKey, startIndex, endIndex)
	// Reserve global and file capacity atomically, without waiting or
	// opening the provider request on the foreground goroutine.
	job := r.source.TryFetch(entry, drive.RangePlan{Offset: startIndex * ChunkSize, Size: (endIndex - startIndex + 1) * ChunkSize, Timeouts: fetchTimeoutsFromContext(ctx), Priority: PriorityLow})
	if job == nil {
		return nil
	}
	if !r.state.reservePrefetch() {
		job.Close()
		return nil
	}
	scope, releaseScope := r.state.scopeFor(ctx, cacheKey)
	prefetchCtx, cancelCause := context.WithCancelCause(context.WithoutCancel(ctx))
	cancel := func() { cancelCause(nil) }

	prefetchCtx = WithPriority(prefetchCtx, PriorityLow)
	prefetchCtx = WithGrantedStreams(prefetchCtx, 1)
	prefetchCtx = WithPrefetchRead(prefetchCtx)

	hint, _ := AccessHintFromContext(ctx)
	trace := r.newReadTrace(prefetchCtx, entry)
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, streams: 1, prefetch: true, progressive: entry.Size > 0, trace: trace, createdAt: time.Now(), done: make(chan struct{}), cancel: func() { cancelCause(drive.ReadSuperseded) }, sessionID: hint.SessionID}
	load.chunks = make([]windowChunk, int(endIndex-startIndex+1))
	for i := range load.chunks {
		load.chunks[i].ready = make(chan struct{})
	}

	if _, exists := r.state.beginWindowLoad(key, load); exists {
		job.Close()
		r.state.releasePrefetch()
		releaseScope()
		cancel()
		return nil
	}

	stopScope := context.AfterFunc(scope.ctx, func() {
		if r.state.abandonWindow(load, 0, true) {
			if reason := scope.cancelReason(); reason == "seek" || reason == "session_release" {
				cancelCause(drive.ReadSuperseded)
			} else {
				cancelCause(drive.ReadCallerCanceled)
			}
		}
	})

	scope.jobs.Add(1)
	go func() {
		defer scope.jobs.Done()
		defer job.Close()
		defer releaseScope()
		defer stopScope()
		started := time.Now()
		activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
			Kind:        "vfs_prefetch",
			Phase:       "fetch_window",
			Path:        DebugOperationName(ctx),
			RemoteID:    entry.ID,
			Offset:      startIndex * ChunkSize,
			Requested:   (endIndex - startIndex + 1) * ChunkSize,
			ChunkIndex:  startIndex,
			WindowStart: startIndex,
			WindowEnd:   endIndex,
			Background:  true,
			ReadIntent:  ReadIntentFromContext(ctx).String(),
			ReadStreams: 1,
			WaitFor:     key,
		})
		defer func() {
			r.observer.DebugFinishActive(activeID)
			r.state.endWindowLoad(load)
			load.finishChunks()
			close(load.done)
			r.state.releasePrefetch()
			cancel()
			job.Close()
		}()
		// Speculative range admission is atomic and never waits for capacity.
		prefetchCtx = drive.WithReadTrace(prefetchCtx, trace)
		prefetchCtx = r.withReadFaults(prefetchCtx)
		// The window is executed through the same streaming FetchStream path
		// the foreground single-stream window uses; the job's admission is
		// reused by the Range inside it, so no second permit is taken.
		var ctxErr error
		if prefetchCtx, ctxErr = job.Context(prefetchCtx); ctxErr != nil {
			load.err = ctxErr
			return
		}
		backend := r.newBackend()
		load.data = make(map[int64][]byte, len(load.chunks))
		load.err = r.source.FetchStream(prefetchCtx, entry, drive.RangePlan{
			Offset:      startIndex * ChunkSize,
			Size:        (endIndex - startIndex + 1) * ChunkSize,
			Timeouts:    fetchTimeoutsFromContext(ctx),
			Priority:    PriorityLow,
			Speculative: true,
		}, func(offset int64, data []byte) {
			index := offset / ChunkSize
			backend.StoreChunk(prefetchCtx, backend.CacheKey(entry), entry, index, data)
			load.data[index] = data
			load.publish(index, data)
		})
		load.extra = trace.Snapshot()
		load.extra = CloneExtra(load.extra)
		if load.extra == nil {
			load.extra = map[string]any{}
		}
		load.extra["read_streams"] = 1
		load.extra["fetch_us"] = time.Since(started).Microseconds()
		recordWindowFetchTimeouts(r.state, true, cacheKey, startIndex, endIndex, load.extra)
		if load.err != nil {
			// A cancellation reason set by delete/close/seek/session release
			// wins over the timeout that raced it: the operation was already
			// going away for another reason.
			reason := ""
			if prefetchCtx.Err() != nil {
				reason = scope.cancelReason()
			}
			if reason == "" && isReadFetchTimeout(load.err) {
				reason = "timeout"
			}
			if reason != "" {
				load.extra["prefetch_cancel_reason"] = reason
			}
			if reason == "timeout" {
				load.extra["cancel_reason"] = "timeout"
			}
		}
		RecordChunkDetail(r.observer, prefetchCtx, entry, "prefetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, load.extra, load.err)
	}()
	return load
}

func (r *Reader) withReadFaults(ctx context.Context) context.Context {
	return drive.WithReadBodyTransform(ctx, func(ctx context.Context, e drive.Entry, offset, size int64, rc io.ReadCloser) (io.ReadCloser, error) {
		return applyReadFault(ctx, r.faults, e, offset, size, rc)
	})
}
