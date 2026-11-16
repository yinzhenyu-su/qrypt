package read

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	if override.MaxStreams > 0 && plan.Intent != ReadIntentInteractive && plan.Intent != ReadIntentProbe {
		plan.MaxStreams = override.MaxStreams
		changed = true
	}
	if override.WindowChunks > 0 && plan.Intent != ReadIntentProbe {
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
		// An adaptive pass warms only its seed window: the controller must not
		// find a full stage window already admitted before it knows the budget.
		r.prefetchWindow(readCtx, entry, offset/ChunkSize+1, firstPrefetchChunks(plan, windowChunks))
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
	consumed := deliveredRange{offset: offset, size: int64(len(data))}
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
		r.schedulePrefetch(readCtx, entry, endChunk, windowChunks, decision.sequential, plan, consumed)
	} else if readPrefetchEnabled(ctx) && plan.PrefetchDepth > 0 && !decision.stale && !hint.Concurrent && !decision.discontinuous {
		// This read does not admit a prefetch yet, but its delivered bytes are
		// still consumption: the budget grows from delivered bytes alone, so the
		// same range earns the same credit however the caller splits it. A
		// discontinuous read is a seek: its old pass is canceled below the gate
		// and a new one starts only after contiguous access is confirmed.
		r.creditSessionDelivery(readCtx, entry, endChunk+1, decision.sequential, windowChunks, plan, consumed)
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
	ctx = withWindowBypass(ctx)
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
	registered := false
	for attempt := 0; attempt <= fetchTimeoutAttempts; attempt++ {
		existing, joined := r.state.beginWindowLoad(key, load)
		if !joined {
			registered = true
			break
		}
		if windowBypassed(ctx, existing) {
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
			extra := windowWaitExtra(existing)
			extra["wait_us"] = util.Now().Sub(started).Microseconds()
			RecordChunkDetail(r.observer, ctx, entry, "wait_window_load_bypass", startIndex, 0, ChunkSize, 0, started, extra, nil)
			r.bypassPrefetch(ctx, existing)
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
			extra := ExtraWithWindow(existing.extra, existing.start, existing.end)
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
		WaitFor:     key,
	})
	r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
	load.data, load.extra, load.err = r.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	fetchFinished := util.Now()
	fetchTimingFromContext(ctx).recordFetch(fetchFinished.Sub(started))
	r.observer.DebugFinishActive(activeID)
	recordWindowFetchTimeouts(r.state, false, cacheKey, startIndex, endIndex, load.extra)
	fetchExtra := ExtraWithWindow(load.extra, startIndex, endIndex)
	// Attach the resolved plan so a detail event explains which stage and
	// window policy produced this fetch, not only the driver-level timing.
	for key, value := range ReadPlanExtra(ctx, int(endIndex-startIndex+1)) {
		if _, exists := fetchExtra[key]; !exists {
			fetchExtra[key] = value
		}
	}
	fetchExtra["fetch_us"] = fetchFinished.Sub(started).Microseconds()
	if isReadFetchTimeout(load.err) {
		fetchExtra["cancel_reason"] = "timeout"
	}
	RecordChunkDetail(r.observer, ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, fetchExtra, load.err)
	close(load.done)

	if registered {
		r.state.endWindowLoad(load)
	}
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}

func (r *Reader) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	ctx = r.withFetchTimeouts(ctx)
	plan, _ := readPlanFromContext(ctx)
	ctx = r.withReadFaults(ctx)
	trace := r.newReadTrace(ctx, entry)
	ctx = drive.WithReadTrace(ctx, trace)
	data, err := r.source.Fetch(ctx, entry, drive.RangePlan{Offset: startIndex * ChunkSize, Size: (endIndex - startIndex + 1) * ChunkSize, Streams: grantedStreams(ctx, plan.MaxStreams), Split: plan.Split, Timeouts: fetchTimeoutsFromContext(ctx), Priority: priority(ctx), Speculative: isPrefetchRead(ctx)})
	if err != nil {
		return nil, trace.Snapshot(), err
	}
	return storeWindowChunks(ctx, r.newBackend(), entry, startIndex, endIndex, data), trace.Snapshot(), nil
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
	load := r.state.findWindow(fid, index, streams, maxChunks)
	if load == nil {
		return nil, false, nil
	}
	if windowBypassed(ctx, load) {
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
		r.bypassPrefetch(ctx, load)
		extra := windowWaitExtra(load)
		extra["wait_us"] = util.Now().Sub(started).Microseconds()
		RecordChunkDetail(r.observer, ctx, drive.Entry{ID: fid}, "wait_window_bypass", index, 0, ChunkSize, 0, started, extra, nil)
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
		"window_start": load.start, "window_end": load.end,
		"window_chunks": load.end - load.start + 1, "window_streams": load.streams,
		"window_prefetch": load.prefetch, "window_session_id": load.sessionID,
	}
	if load.prefetch {
		extra["wait_budget_us"] = prefetchWaitBudget.Microseconds()
	}
	if !load.createdAt.IsZero() {
		extra["window_age_us"] = time.Since(load.createdAt).Microseconds()
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
	return r.state.findWindow(cacheKey, index, streams, 0) != nil
}

func (r *Reader) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return r.state.hotChunk(cacheKey, index)
}

func (r *Reader) putHotChunk(cacheKey string, index int64, data []byte) {
	r.state.putHotChunk(cacheKey, index, data)
}

// --- prefetch ---

// prefetchWaitBudget bounds waiting without new bytes in the target block.
// A progressing single-stream prefetch keeps its work; a stalled or distant
// target is taken over within this interval. Shared idle/total limits still apply.
// It is a variable so a test can exercise the bypass without waiting three
// seconds.
var prefetchWaitBudget = 3 * time.Second

// prefetchWindowChunks is the speculative window size for a planned read: the
// driver's stage suggestion when it made one, otherwise the foreground window.
func prefetchWindowChunks(plan ReadPlan, windowChunks int) int {
	if plan.PrefetchChunks > 0 {
		return plan.PrefetchChunks
	}
	return windowChunks
}

// firstPrefetchChunks sizes the offset-zero warm-up window. A consumption-credit
// plan starts at its seed, so the warm-up cannot admit a full stage window
// ahead of the controller; every other plan keeps its stage window.
func firstPrefetchChunks(plan ReadPlan, windowChunks int) int {
	if plan.usesConsumptionCredit() {
		return max(1, int(plan.seedBytes()/ChunkSize))
	}
	return prefetchWindowChunks(plan, windowChunks)
}

// schedulePrefetch advances a known session's bounded lookahead; callers
// without session identity keep one bounded scheduling burst. consumed is the
// caller's successful delivery and is the only input that may grow the
// session's forward budget.
func (r *Reader) schedulePrefetch(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int, sequential bool, plan ReadPlan, consumed deliveredRange) {
	plan = r.sessionPlan(ctx, windowChunks, sequential, plan)
	if r.updateSessionPrefetch(ctx, entry, endChunk+1, plan, consumed, true) {
		return
	}
	for i := 0; i < plan.PrefetchDepth; i++ {
		r.prefetchWindow(ctx, entry, endChunk+1+int64(i*plan.PrefetchChunks), plan.PrefetchChunks)
	}
}

// sessionPlan resolves the stage a session will execute. It is shared by prefetch
// admission and by delivery accounting so both see the same plan.
func (r *Reader) sessionPlan(ctx context.Context, windowChunks int, sequential bool, plan ReadPlan) ReadPlan {
	stage := plan.Stage
	if sequential {
		stage = StageSequential
	}
	if stage != plan.Stage {
		return r.resolvePlan(ctx, windowChunks, stage)
	}
	return plan
}

// creditSessionDelivery records one successful delivery with the session's
// forward budget without admitting a window. Credit is adaptive-only state and
// needs an established session; a read that never starts one is a no-op.
func (r *Reader) creditSessionDelivery(ctx context.Context, entry drive.Entry, cursor int64, sequential bool, windowChunks int, plan ReadPlan, consumed deliveredRange) {
	if consumed.size <= 0 || !plan.usesConsumptionCredit() {
		return
	}
	r.updateSessionPrefetch(ctx, entry, cursor, r.sessionPlan(ctx, windowChunks, sequential, plan), consumed, false)
}

func (r *Reader) PrefetchAdjacentChunks(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int, sequential bool) {
	r.prefetchAdjacentChunks(ctx, entry, endChunk, windowChunks, sequential, 0)
}

// prefetchAdjacentChunks issues the legacy lookahead: windows the size of the
// foreground window, or the bounded prefetch slots when the foreground reads
// one chunk at a time. windows > 0 replaces only the count, so an explicit
// stage depth applies without also requiring an explicit prefetch window.
func (r *Reader) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int, sequential bool, windows int) {
	if windowChunks <= 0 {
		return
	}
	prefetchChunks := windowChunks
	if windowChunks == 1 {
		// Keep the foreground miss to one chunk for TTFB, then fill the
		// bounded prefetch slots so sequential FUSE reads pipeline remote GETs.
		if windows <= 0 {
			windows = r.state.adaptivePrefetchLimit(sequential)
		}
		if sequential {
			prefetchChunks = SequentialPrefetchChunks
		}
	}
	if windows <= 0 {
		// A window wider than one chunk keeps the single lookahead window the
		// legacy rule opens for it.
		windows = 1
	}
	for i := 0; i < windows; i++ {
		startIndex := endChunk + 1 + int64(i*prefetchChunks)
		r.prefetchWindow(ctx, entry, startIndex, prefetchChunks)
	}
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
		// One-shot and ReadStream callers retain their merged window size.
		// Only the session controller owns a consumption-bounded horizon.
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
		}()
		// Speculative range admission is atomic and never waits for capacity.
		prefetchCtx = drive.WithReadTrace(prefetchCtx, trace)
		prefetchCtx = r.withReadFaults(prefetchCtx)
		backend := r.newBackend()
		load.data = make(map[int64][]byte, len(load.chunks))
		load.err = job.RunChunks(prefetchCtx, func(offset int64, data []byte) {
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

type windowBypassKey struct{}
type windowBypass struct{ loads sync.Map }

func withWindowBypass(ctx context.Context) context.Context {
	if ctx.Value(windowBypassKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, windowBypassKey{}, &windowBypass{})
}
func windowBypassed(ctx context.Context, load *windowLoad) bool {
	b, _ := ctx.Value(windowBypassKey{}).(*windowBypass)
	if b == nil {
		return false
	}
	_, ok := b.loads.Load(load)
	return ok
}
func (r *Reader) bypassPrefetch(ctx context.Context, load *windowLoad) {
	if b, _ := ctx.Value(windowBypassKey{}).(*windowBypass); b != nil {
		b.loads.Store(load, struct{}{})
	}
	hint, _ := AccessHintFromContext(ctx)
	if r.state.abandonWindow(load, hint.SessionID, false) && load.cancel != nil {
		load.cancel()
	}
}
