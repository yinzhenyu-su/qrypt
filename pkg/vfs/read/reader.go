package read

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

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
	profile  drive.ReadProfile
	policy   *CachePolicy
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
	// Profile carries the effective read scheduling profile for this mount.
	// The zero value falls back to drive.DefaultReadProfile. A mount may
	// override the driver default through its read_profile config.
	Profile drive.ReadProfile
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
	if deps.Profile == (drive.ReadProfile{}) {
		deps.Profile = drive.DefaultReadProfile()
	}
	r := &Reader{
		host:     deps.Host,
		state:    deps.State,
		observer: deps.Observer,
		health:   deps.Health,
		counters: deps.Counters,
		profile:  deps.Profile,
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

// resolvePlan resolves the caller's intent and the driver profile into the
// concrete plan carried by this read's context.
func (r *Reader) resolvePlan(ctx context.Context, entry drive.Entry, defaultWindowChunks int) ReadPlan {
	return PlanResolver{Profile: r.profile}.Resolve(ReadIntentFromContext(ctx), entry.Size, defaultWindowChunks)
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
func (r *Reader) applyPlanOverride(plan *ReadPlan, windowChunks *int) bool {
	override, ok := r.state.planOverride()
	if !ok {
		return false
	}
	if override.MaxStreams > 0 {
		plan.MaxStreams = override.MaxStreams
	}
	if override.WindowChunks > 0 {
		*windowChunks = override.WindowChunks
		plan.WindowChunks = override.WindowChunks
	}
	return override.MaxStreams > 0 || override.WindowChunks > 0
}

// perFileLimit returns the stable concurrency budget for one file. A partial
// window may use fewer streams, but the per-file budget never changes while
// permits are in flight. A measurement override may raise it.
func (r *Reader) perFileLimit() int {
	limit := r.profile.BulkStreams
	if override, ok := r.state.planOverride(); ok && override.MaxStreams > limit {
		limit = override.MaxStreams
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

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
			// A seek makes the old lookahead irrelevant. Cancel it before the
			// target load competes for bandwidth and read slots.
			r.state.cancelPrefetch(cacheKey)
		}
		if !decision.stale && !hint.Concurrent && decision.discontinuous &&
			offset%ChunkSize != 0 && size > 0 && size <= ChunkSize {
			// macOS FUSE commonly begins a seek with a small aligned-down
			// request. Fetch both touched chunks in one Range request so the
			// seek pays one CDN response-header delay instead of two.
			windowChunks = 2
		}
	}
	plan := r.resolvePlan(ctx, entry, windowChunks)
	windowChunks = plan.WindowChunks
	loadKey := LoadKey(r.host, entry)
	readCtx = WithReadPlan(readCtx, plan)
	readCtx = WithGrantedStreams(readCtx, r.state.scheduler.GrantStreams(plan.MaxStreams, loadKey, r.perFileLimit()))
	readCtx = applyIntentPriority(readCtx, plan)
	if plan.Intent == ReadIntentUnspecified && hinted && !hint.Concurrent && !decision.stale &&
		decision.sequential && entry.Size >= r.profile.ParallelThreshold {
		// A sustained sequential FUSE stream is the bulk case even though the
		// mount does not declare an intent. Keep cache writes and the FUSE
		// window policy: only the stream count changes.
		upgraded := PlanResolver{Profile: r.profile}.Resolve(ReadIntentBulk, entry.Size, windowChunks)
		upgraded.CacheWrites = true
		upgraded.WindowChunks = windowChunks
		plan = upgraded
		readCtx = WithReadPlan(readCtx, plan)
		readCtx = WithGrantedStreams(readCtx, r.state.scheduler.GrantStreams(plan.MaxStreams, loadKey, r.perFileLimit()))
		readCtx = applyIntentPriority(readCtx, plan)
	}
	if r.applyPlanOverride(&plan, &windowChunks) {
		readCtx = WithReadPlan(readCtx, plan)
		readCtx = WithGrantedStreams(readCtx, r.state.scheduler.GrantStreams(plan.MaxStreams, loadKey, r.perFileLimit()))
		readCtx = applyIntentPriority(readCtx, plan)
	}
	readCtx = WithCacheAccess(readCtx, access)
	firstWindowPrefetch := (!hinted && offset%ChunkSize != 0) || (hinted && offset == 0)
	if readPrefetchEnabled(ctx) && plan.PrefetchDepth > 0 && !decision.stale && !hint.Concurrent && !decision.sequential && firstWindowPrefetch && windowChunks == 1 {
		// Warm the first lookahead window for a new file stream. Non-zero
		// offsets are treated as seeks until contiguous access is observed, so
		// random probes do not trigger speculative network reads.
		r.prefetchWindow(readCtx, entry, offset/ChunkSize+1, windowChunks)
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
		r.PrefetchAdjacentChunks(readCtx, entry, endChunk, windowChunks, decision.sequential)
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
		out.Write(chunk)
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
	AcquireSlot(ctx context.Context) (func(), error)
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

func (rt *stateRuntime) AcquireSlot(ctx context.Context) (func(), error) {
	return rt.reader.acquireReadSlot(ctx)
}

// readWindowBackend abstracts the driver read + cache store for
// fetchChunkWindow, testable with a fake.
type readWindowBackend interface {
	Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
	CacheKey(entry drive.Entry) string
	StoreChunk(ctx context.Context, cacheKey string, entry drive.Entry, index int64, chunk []byte)
}

func (r *Reader) newBackend() readWindowBackend {
	return &readerBackend{reader: r}
}

type readerBackend struct {
	reader *Reader
}

func (b *readerBackend) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return b.reader.host.DriverRead(ctx, entry, offset, size)
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
	if streams > 1 && r.state.isNonParallel(LoadKey(r.host, entry)) {
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
		select {
		case <-existing.done:
			waitFinished := util.Now()
			extra := ExtraWithWindow(existing.extra, existing.start, existing.end)
			extra["wait_us"] = waitFinished.Sub(started).Microseconds()
			if existing.err != nil {
				// A timed-out prefetch is a silent cancellation for the
				// prefetch itself, but a foreground read still needs its data.
				// Take over and fetch the window instead of failing the read.
				if existing.prefetch && isReadFetchTimeout(existing.err) &&
					attempt < fetchTimeoutAttempts && ctx.Err() == nil {
					extra["cancel_reason"] = "timeout"
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
		case <-ctx.Done():
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
	releaseSlot := func() {}
	if streams <= 1 {
		var slotErr error
		releaseSlot, slotErr = r.acquireReadSlot(ctx)
		if slotErr != nil {
			load.err = slotErr
			r.observer.DebugFinishActive(activeID)
			close(load.done)
			if registered {
				r.state.endWindowLoad(key)
			}
			return nil, slotErr
		}
	}
	r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
	load.data, load.extra, load.err = r.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	releaseSlot()
	fetchFinished := util.Now()
	fetchTimingFromContext(ctx).recordFetch(fetchFinished.Sub(started))
	r.observer.DebugFinishActive(activeID)
	recordWindowFetchTimeouts(r.state, false, cacheKey, startIndex, endIndex, load.extra)
	fetchExtra := ExtraWithWindow(load.extra, startIndex, endIndex)
	fetchExtra["fetch_us"] = fetchFinished.Sub(started).Microseconds()
	if isReadFetchTimeout(load.err) {
		fetchExtra["cancel_reason"] = "timeout"
	}
	RecordChunkDetail(r.observer, ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, fetchExtra, load.err)
	close(load.done)

	if registered {
		r.state.endWindowLoad(key)
	}
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}

type windowRange struct {
	start int64
	end   int64
}

type windowRangeResult struct {
	data  []byte
	extra map[string]any
	err   error
}

type parallelLimiter interface {
	acquireParallel(ctx context.Context, key string, limit int) (func(), error)
	markNonParallel(key string)
	isNonParallel(key string) bool
}

func (r *Reader) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	ctx = r.withFetchTimeouts(ctx)
	plan, _ := readPlanFromContext(ctx)
	streams := grantedStreams(ctx, plan.MaxStreams)
	key := LoadKey(r.host, entry)
	if streams > 1 && r.state.isNonParallel(key) {
		streams = 1
	}
	var slotAcquire func(context.Context) (func(), error)
	if streams > 1 {
		slotAcquire = r.acquireReadSlot
	}
	return fetchChunkWindowWithPlan(ctx, entry, startIndex, endIndex, r.newBackend(), streams, r.perFileLimit(), slotAcquire, r.state, key)
}

func fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, backend readWindowBackend) (map[int64][]byte, map[string]any, error) {
	return fetchChunkWindowWithPlan(ctx, entry, startIndex, endIndex, backend, 1, 1, nil, nil, "")
}

func fetchChunkWindowWithPlan(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, backend readWindowBackend, streams, perFileLimit int, slotAcquire func(context.Context) (func(), error), limiter parallelLimiter, key string) (map[int64][]byte, map[string]any, error) {
	if streams <= 1 {
		if slotAcquire != nil {
			release, err := slotAcquire(ctx)
			if err != nil {
				return nil, nil, err
			}
			defer release()
		}
		data, extra, err := readWindowRange(ctx, entry, startIndex, endIndex, backend)
		if err != nil {
			return nil, extra, err
		}
		return storeWindowChunks(ctx, backend, entry, startIndex, endIndex, data), extra, nil
	}
	chunkCount := endIndex - startIndex + 1
	if chunkCount <= 1 {
		return fetchChunkWindowWithPlan(ctx, entry, startIndex, endIndex, backend, 1, perFileLimit, slotAcquire, limiter, key)
	}
	if streams > int(chunkCount) {
		streams = int(chunkCount)
	}
	ranges := splitWindow(startIndex, endIndex, streams)
	results := make([]windowRangeResult, len(ranges))
	group, groupCtx := errgroup.WithContext(ctx)
	for i, span := range ranges {
		i, span := i, span
		group.Go(func() error {
			if limiter != nil && key != "" {
				release, err := limiter.acquireParallel(groupCtx, key, perFileLimit)
				if err != nil {
					return err
				}
				defer release()
			}
			if slotAcquire != nil {
				release, err := slotAcquire(groupCtx)
				if err != nil {
					return err
				}
				defer release()
			}
			strictCtx := drive.WithStrictRange(groupCtx)
			results[i].data, results[i].extra, results[i].err = readWindowRange(strictCtx, entry, span.start, span.end, backend)
			return results[i].err
		})
	}
	if err := group.Wait(); err != nil {
		unsupported := false
		for _, result := range results {
			if errors.Is(result.err, drive.ErrUnsupported) {
				unsupported = true
				break
			}
		}
		if unsupported && limiter != nil {
			limiter.markNonParallel(key)
			return fetchChunkWindowWithPlan(ctx, entry, startIndex, endIndex, backend, 1, perFileLimit, slotAcquire, limiter, key)
		}
		return nil, nil, err
	}
	chunks := make(map[int64][]byte, chunkCount)
	for i, span := range ranges {
		for index, chunk := range storeWindowChunks(ctx, backend, entry, span.start, span.end, results[i].data) {
			chunks[index] = chunk
		}
	}
	return chunks, aggregateWindowExtra(results, streams), nil
}

const (
	// fetchTimeoutAttempts is the total number of tries for one window range
	// after an idle stall: the original Range GET plus one idempotent retry. A
	// Range GET is safe to replay, and the window is only assembled after a
	// full range succeeds, so a retry never mixes partial data.
	fetchTimeoutAttempts = 2
	// fetchStatusAttempts is the total number of tries for one window range
	// when the provider answers with a retryable status (5xx, 408, 429). A
	// transient provider hiccup then costs one or two extra requests instead of
	// a failed FUSE read, while a persistent failure still fails fast: three
	// attempts bound the extra latency at two backoffs plus two requests.
	fetchStatusAttempts = 3
	// fetchStatusBackoffBase is the wait before the first status replay; it
	// doubles per replay and is capped by fetchStatusBackoffMax. The wait is
	// charged to the range's total budget, which also bounds the sum.
	fetchStatusBackoffBase = 200 * time.Millisecond
	fetchStatusBackoffMax  = 2 * time.Second
)

func readWindowRange(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, backend readWindowBackend) ([]byte, map[string]any, error) {
	offset := startIndex * ChunkSize
	size := (endIndex - startIndex + 1) * ChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil, nil
	}
	timeouts := fetchTimeoutsFromContext(ctx)
	// One deadline covers every attempt: an idle-stall replay or a status
	// backoff may only spend what is left of the range's total budget.
	deadline := time.Now().Add(timeouts.Total)
	var extra map[string]any
	var lastErr error
	attempts := 0
	timeoutCount := 0
	timeoutKind := drive.FetchTimeoutIdle
	statusRetries := 0
	statusReason := ""
	var backoffTotal time.Duration
	recordRetries := func() {
		if extra == nil {
			return
		}
		if attempts > 1 {
			extra["fetch_attempts"] = attempts
			extra["fetch_retried"] = true
		}
		if timeoutCount > 0 {
			extra["fetch_timeouts"] = timeoutCount
			extra["fetch_timeout_kind"] = string(timeoutKind)
		}
		if statusRetries > 0 {
			extra["fetch_status_retries"] = statusRetries
			extra["fetch_status_reason"] = statusReason
			extra["fetch_backoff_ms"] = DurationMillis(backoffTotal)
		}
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attempts++
		data, attemptExtra, err := readWindowRangeAttempt(ctx, entry, offset, size, timeouts.Idle, remaining, backend)
		extra = attemptExtra
		if err == nil {
			recordRetries()
			return data, extra, nil
		}
		lastErr = err
		var timeout *drive.FetchTimeoutError
		if errors.As(err, &timeout) {
			timeoutCount++
			timeoutKind = timeout.Kind
			// Only an idle stall is worth replaying: a total deadline has no
			// budget left, and other failures are not the timeout case.
			if timeout.Kind == drive.FetchTimeoutIdle && timeoutCount < fetchTimeoutAttempts && ctx.Err() == nil {
				continue
			}
			recordRetries()
			return nil, extra, err
		}
		reason := readRetryReason(err)
		if reason == "" || statusRetries+1 >= fetchStatusAttempts || ctx.Err() != nil {
			recordRetries()
			return nil, extra, err
		}
		statusRetries++
		statusReason = reason
		backoff := fetchStatusBackoff(statusRetries)
		if backoff >= time.Until(deadline) {
			// The wait would outlive the range's budget: reporting the provider
			// failure is more useful than the timeout we would cause.
			recordRetries()
			return nil, extra, err
		}
		if sleepErr := sleepContext(ctx, backoff); sleepErr != nil {
			recordRetries()
			return nil, extra, sleepErr
		}
		backoffTotal += backoff
	}
	recordRetries()
	if extra == nil {
		extra = map[string]any{}
	}
	if lastErr != nil {
		return nil, extra, lastErr
	}
	return nil, extra, &drive.FetchTimeoutError{Kind: drive.FetchTimeoutTotal, Limit: timeouts.Total}
}

// readRetryReason reports why a failed Range GET is worth replaying, or "" when
// it is not. Range GETs are idempotent, so a provider hiccup costs one extra
// request instead of a failed read; permanent failures (auth, permission, not
// found, invalid input, unsupported range) keep their fast path.
func readRetryReason(err error) string {
	if err == nil || drive.IsNonRetryable(err) {
		return ""
	}
	var status *drive.HTTPStatusError
	if errors.As(err, &status) {
		switch {
		case status.Status == http.StatusTooManyRequests:
			return "rate_limited"
		case status.Status == http.StatusRequestTimeout:
			return "request_timeout"
		case status.Status >= 500:
			return "status_5xx"
		}
	}
	if errors.Is(err, drive.ErrRateLimit) {
		return "rate_limited"
	}
	return ""
}

// fetchStatusBackoff returns the wait before the n-th status replay (1-based):
// base, base*2, base*4, capped at fetchStatusBackoffMax.
func fetchStatusBackoff(retries int) time.Duration {
	delay := fetchStatusBackoffBase
	for i := 1; i < retries && delay < fetchStatusBackoffMax; i++ {
		delay *= 2
	}
	return min(delay, fetchStatusBackoffMax)
}

// sleepContext waits for d, or returns early when ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readWindowRangeAttempt performs one Range GET bounded by the idle and total
// limits. total is the remaining share of the fetch deadline, so opening the
// request and draining the body share one budget.
func readWindowRangeAttempt(ctx context.Context, entry drive.Entry, offset, size int64, idle, total time.Duration, backend readWindowBackend) ([]byte, map[string]any, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	openStarted := util.Now()
	rc, err := backend.Read(attemptCtx, entry, offset, size)
	openFinished := util.Now()
	extra := map[string]any{"driver_open_ms": DurationMillis(openFinished.Sub(openStarted))}
	if err != nil {
		return nil, extra, classifyFetchError(ctx, err, total)
	}
	defer rc.Close()
	bodyStarted := util.Now()
	data, err := drive.ReadBodyLimited(attemptCtx, cancel, rc, size, idle)
	bodyFinished := util.Now()
	extra["driver_body_ms"] = DurationMillis(bodyFinished.Sub(bodyStarted))
	closeStarted := util.Now()
	closeErr := rc.Close()
	closeFinished := util.Now()
	extra["driver_close_ms"] = DurationMillis(closeFinished.Sub(closeStarted))
	if err != nil {
		return nil, extra, classifyFetchError(ctx, err, total)
	}
	if closeErr != nil {
		return nil, extra, closeErr
	}
	return data, extra, nil
}

// classifyFetchError maps a context deadline reached while fetching to the
// timeout error semantic, while preserving the caller's own cancellation.
func classifyFetchError(parent context.Context, err error, limit time.Duration) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if drive.IsFetchTimeout(err) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &drive.FetchTimeoutError{Kind: drive.FetchTimeoutTotal, Limit: limit, Elapsed: limit}
	}
	return err
}

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

func splitWindow(startIndex, endIndex int64, streams int) []windowRange {
	count := endIndex - startIndex + 1
	if streams > int(count) {
		streams = int(count)
	}
	if streams < 1 {
		streams = 1
	}
	base := count / int64(streams)
	remainder := count % int64(streams)
	ranges := make([]windowRange, 0, streams)
	cursor := startIndex
	for i := 0; i < streams; i++ {
		size := base
		if int64(i) < remainder {
			size++
		}
		ranges = append(ranges, windowRange{start: cursor, end: cursor + size - 1})
		cursor += size
	}
	return ranges
}

func aggregateWindowExtra(results []windowRangeResult, streams int) map[string]any {
	extra := map[string]any{"parallel_streams": streams}
	for _, field := range []string{"driver_open_ms", "driver_body_ms", "driver_close_ms"} {
		var maxValue int64
		for _, result := range results {
			if value, ok := result.extra[field].(int64); ok && value > maxValue {
				maxValue = value
			}
		}
		extra[field] = maxValue
	}
	return extra
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
	load := r.state.findWindow(fid, index, streams)
	if load == nil {
		return nil, false, nil
	}
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
	select {
	case <-load.done:
		if load.err != nil {
			// A timed-out prefetch is a miss for a foreground reader: let the
			// caller load the window itself instead of surfacing a background
			// failure as its own.
			if load.prefetch && isReadFetchTimeout(load.err) {
				RecordChunkDetail(r.observer, ctx, drive.Entry{ID: fid}, "wait_window", index, 0, ChunkSize, 0, started, map[string]any{
					"wait_us":       util.Now().Sub(started).Microseconds(),
					"cancel_reason": "timeout",
				}, load.err)
				return nil, false, nil
			}
			if errors.Is(load.err, context.Canceled) {
				return nil, false, nil
			}
			return nil, true, load.err
		}
		return load.data[index], true, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
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
	return r.state.findWindow(cacheKey, index, streams) != nil
}

func (r *Reader) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return r.state.hotChunk(cacheKey, index)
}

func (r *Reader) putHotChunk(cacheKey string, index int64, data []byte) {
	r.state.putHotChunk(cacheKey, index, data)
}

func (r *Reader) acquireReadSlot(ctx context.Context) (func(), error) {
	return r.state.scheduler.AcquireSlot(ctx, priority(ctx))
}

// --- prefetch ---

func (r *Reader) PrefetchAdjacentChunks(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int, sequential bool) {
	if windowChunks <= 0 {
		return
	}
	windows := 1
	prefetchChunks := windowChunks
	if windowChunks == 1 {
		// Keep the foreground miss to one chunk for TTFB, then fill the
		// bounded prefetch slots so sequential FUSE reads pipeline remote GETs.
		windows = r.state.adaptivePrefetchLimit(sequential)
		if sequential {
			prefetchChunks = SequentialPrefetchChunks
		}
	}
	for i := 0; i < windows; i++ {
		startIndex := endChunk + 1 + int64(i*prefetchChunks)
		r.prefetchWindow(ctx, entry, startIndex, prefetchChunks)
	}
}

func (r *Reader) prefetchWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) {
	if startIndex < 0 || count <= 0 {
		return
	}
	if entry.Size > 0 && startIndex*ChunkSize >= entry.Size {
		return
	}
	cacheKey := r.host.ReadCacheKey(entry)
	if cacheKey == "" {
		return
	}
	ctx = r.withFetchTimeouts(ctx)
	plannedEndIndex := startIndex + int64(count) - 1
	for startIndex <= plannedEndIndex {
		if entry.Size > 0 && startIndex*ChunkSize >= entry.Size {
			return
		}
		if r.readChunkAvailableFor(cacheKey, startIndex, 1) {
			startIndex++
			continue
		}
		break
	}
	if startIndex > plannedEndIndex {
		return
	}
	// Preserve the requested window size when cached or in-flight chunks at
	// its head were skipped. This keeps sequential prefetches at 2 MiB instead
	// of shrinking them to a 1 MiB Range request.
	maxEndIndex := startIndex + int64(count) - 1
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
		return
	}
	key := WindowKey(cacheKey, startIndex, endIndex)
	if !r.state.reservePrefetch() {
		return
	}
	scope, releaseScope := r.state.scopeFor(ctx, cacheKey)
	prefetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopScope := context.AfterFunc(scope.ctx, cancel)
	prefetchCtx = WithPriority(prefetchCtx, PriorityLow)
	prefetchCtx = WithGrantedStreams(prefetchCtx, 1)

	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, streams: 1, prefetch: true, done: make(chan struct{})}
	if _, exists := r.state.beginWindowLoad(key, load); exists {
		r.state.releasePrefetch()
		stopScope()
		releaseScope()
		cancel()
		return
	}

	go func() {
		defer releaseScope()
		defer stopScope()
		started := time.Now()
		activeID := r.observer.DebugBeginActive(vfstypes.DebugActiveOp{
			Kind:        "vfs_prefetch",
			Phase:       "acquire_slot",
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
		releaseSlot, err := r.acquireReadSlot(prefetchCtx)
		if err != nil {
			load.err = err
			RecordChunkDetail(r.observer, prefetchCtx, entry, "prefetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, 0, started, nil, err)
			r.observer.DebugFinishActive(activeID)
			r.state.endWindowLoad(key)
			close(load.done)
			r.state.releasePrefetch()
			cancel()
			return
		}
		defer func() {
			releaseSlot()
			r.observer.DebugFinishActive(activeID)
			r.state.endWindowLoad(key)
			close(load.done)
			r.state.releasePrefetch()
			cancel()
		}()
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
		load.data, load.extra, load.err = fetchChunkWindow(prefetchCtx, entry, startIndex, endIndex, r.newBackend())
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
}
