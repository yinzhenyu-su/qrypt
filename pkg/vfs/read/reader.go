package read

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
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
}

// ReaderDeps is the explicit dependency set for a read-domain reader.
// Observer and Health are optional: nil values fall back to no-op sinks so
// instrumentation and statistics never affect correctness. Keeping every
// dependency in one struct (rather than positional arguments) lets the set
// grow without reordering call sites.
type ReaderDeps struct {
	Host     Host
	State    *State
	Observer ReadObserver
	Health   HealthRecorder
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
	return &Reader{
		host:     deps.Host,
		state:    deps.State,
		observer: deps.Observer,
		health:   deps.Health,
	}
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
			op.Phase = "staging_flush"
			op.RemoteID = pending.FID
		})
		if err := r.host.FlushStaging(pending.LocalPath); err != nil {
			r.observer.DebugFinishActive(activeID)
			r.observer.DebugRecordRead(opID, path, pending.FID, offset, size, 0, "staging", 0, 0, 0, started, nil, err)
			return nil, err
		}
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) {
			op.Phase = "staging_open"
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
	windowChunks := readWindowChunks(offset, size)
	cacheKey := r.host.ReadCacheKey(entry)
	hint, hinted := AccessHintFromContext(ctx)
	decision := accessDecision{}
	if hinted {
		decision = r.state.observeReadAccess(cacheKey, hint, offset, size)
		if !decision.stale && !hint.Concurrent && decision.discontinuous &&
			offset%ChunkSize != 0 && size > 0 && size <= ChunkSize {
			// macOS FUSE commonly begins a seek with a small aligned-down
			// request. Fetch both touched chunks in one Range request so the
			// seek pays one CDN response-header delay instead of two.
			windowChunks = 2
		}
	}
	firstWindowPrefetch := (!hinted && offset%ChunkSize != 0) || (hinted && offset == 0)
	if readPrefetchEnabled(ctx) && !decision.stale && !hint.Concurrent && !decision.sequential && firstWindowPrefetch && windowChunks == 1 {
		// Warm the first lookahead window for a new file stream. Non-zero
		// offsets are treated as seeks until contiguous access is observed, so
		// random probes do not trigger speculative network reads.
		r.prefetchWindow(readCtx, entry, offset/ChunkSize+1, windowChunks)
	}
	data, startChunk, endChunk, err := r.readRange(readCtx, entry, offset, size, windowChunks)
	hitsAfter, missesAfter := r.observer.DebugCacheCounters()
	if err != nil {
		r.observer.DebugFinishActive(activeID)
		r.observer.DebugRecordRead(opID, path, entry.ID, offset, size, 0, "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, 0, started, WindowExtra(windowChunks), err)
		return nil, err
	}
	if !hinted {
		decision = r.state.observeReadAccess(cacheKey, AccessHint{}, offset, int64(len(data)))
	} else if !r.state.readAccessCurrent(cacheKey, hint) {
		decision.stale = true
	}
	if readPrefetchEnabled(ctx) && !decision.stale && !hint.Concurrent && (!hinted || decision.sequential || offset == 0) {
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
	r.observer.DebugRecordRead(opID, path, entry.ID, offset, size, int64(len(data)), "remote", hitsAfter-hitsBefore, missesAfter-missesBefore, chunks, started, WindowExtra(windowChunks), nil)
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
	FlushStaging(localPath string) error
	ChunkAvailable(cacheKey string, index int64) bool
	GetChunkWithRange(cacheKey string, index, start, size int64) ([]byte, []byte, bool, error)
	GetChunkRange(cacheKey string, index, start, size int64) ([]byte, bool, error)
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

func (rt *stateRuntime) FlushStaging(localPath string) error {
	return rt.reader.host.FlushStaging(localPath)
}

func (rt *stateRuntime) ChunkAvailable(cacheKey string, index int64) bool {
	return rt.reader.readChunkAvailable(cacheKey, index)
}

func (rt *stateRuntime) GetChunkWithRange(cacheKey string, index, start, size int64) ([]byte, []byte, bool, error) {
	if rt.reader.state.cache == nil {
		return nil, nil, false, nil
	}
	return rt.reader.state.cache.GetChunkWithRange(cacheKey, index, start, size)
}

func (rt *stateRuntime) GetChunkRange(cacheKey string, index, start, size int64) ([]byte, bool, error) {
	if rt.reader.state.cache == nil {
		return nil, false, nil
	}
	return rt.reader.state.cache.GetChunkRange(cacheKey, index, start, size)
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
	StoreChunk(cacheKey string, entry drive.Entry, index int64, chunk []byte)
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

func (b *readerBackend) StoreChunk(cacheKey string, entry drive.Entry, index int64, chunk []byte) {
	if cacheKey == "" {
		return
	}
	b.reader.putHotChunk(cacheKey, index, chunk)
	if b.reader.state.cache != nil {
		b.reader.state.cache.PutChunkAsync(cacheKey, entry.Size, index, chunk)
	}
}

func (r *Reader) readChunkRange(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int) ([]byte, error) {
	return r.readChunkRangeWithRuntime(ctx, entry, index, start, size, windowChunks, r.newRuntime())
}

func (r *Reader) readChunkRangeWithRuntime(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int, runtime readRuntime) ([]byte, error) {
	cacheKey := runtime.CacheKey(entry)
	if cacheKey != "" {
		hotStarted := util.Now()
		if hot, ok := runtime.HotChunk(cacheKey, index); ok {
			runtime.AddCacheHit()
			data := sliceChunkRange(hot, start, size)
			RecordChunkDetail(r.observer, ctx, entry, "cache_hot_hit", index, start, size, int64(len(data)), hotStarted, CacheLookupExtra("hot", hotStarted, nil), nil)
			return data, nil
		}
		if shouldPromoteCachedRange(size) && runtime.ShouldPromoteCachedRange(cacheKey, index) {
			started := util.Now()
			if cached, chunk, ok, err := runtime.GetChunkWithRange(cacheKey, index, start, size); err != nil {
				RecordChunkDetail(r.observer, ctx, entry, "cache_range_promote", index, start, size, 0, started, CacheLookupExtra("range_promote", started, nil), err)
				return nil, err
			} else if ok {
				if len(chunk) > 0 {
					runtime.PutHotChunk(cacheKey, index, chunk)
				}
				RecordChunkDetail(r.observer, ctx, entry, "cache_range_promote", index, start, size, int64(len(cached)), started, CacheLookupExtra("range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
				return cached, nil
			}
		}
		started := util.Now()
		if cached, ok, err := runtime.GetChunkRange(cacheKey, index, start, size); err != nil {
			RecordChunkDetail(r.observer, ctx, entry, "cache_range_hit", index, start, size, 0, started, CacheLookupExtra("range", started, nil), err)
			return nil, err
		} else if ok {
			runtime.RecordCachedRangeHit(cacheKey, index, size)
			RecordChunkDetail(r.observer, ctx, entry, "cache_range_hit", index, start, size, int64(len(cached)), started, CacheLookupExtra("range", started, nil), nil)
			return cached, nil
		}
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
		if cacheKey != "" {
			if shouldPromoteCachedRange(size) && runtime.ShouldPromoteCachedRange(cacheKey, index) {
				started := util.Now()
				if cached, chunk, ok, err := runtime.GetChunkWithRange(cacheKey, index, start, size); err != nil {
					RecordChunkDetail(r.observer, ctx, entry, "wait_window_cache_promote", index, start, size, 0, started, CacheLookupExtra("wait_window_range_promote", started, nil), err)
					return nil, err
				} else if ok {
					if len(chunk) > 0 {
						runtime.PutHotChunk(cacheKey, index, chunk)
					}
					RecordChunkDetail(r.observer, ctx, entry, "wait_window_cache_promote", index, start, size, int64(len(cached)), started, CacheLookupExtra("wait_window_range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
					return cached, nil
				}
			}
			started := util.Now()
			if cached, ok, err := runtime.GetChunkRange(cacheKey, index, start, size); err != nil {
				RecordChunkDetail(r.observer, ctx, entry, "wait_window_cache_hit", index, start, size, 0, started, CacheLookupExtra("wait_window_range", started, nil), err)
				return nil, err
			} else if ok {
				runtime.RecordCachedRangeHit(cacheKey, index, size)
				RecordChunkDetail(r.observer, ctx, entry, "wait_window_cache_hit", index, start, size, int64(len(cached)), started, CacheLookupExtra("wait_window_range", started, nil), nil)
				return cached, nil
			}
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
	if cacheKey != "" {
		for endIndex > startIndex && r.readChunkAvailable(cacheKey, endIndex) {
			endIndex--
		}
	}
	key := WindowKey(LoadKey(r.host, entry), startIndex, endIndex)
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	if load, ok := r.state.beginWindowLoad(key, load); ok {
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
		defer r.observer.DebugFinishActive(activeID)
		select {
		case <-load.done:
			extra := ExtraWithWindow(load.extra, load.start, load.end)
			if load.err != nil {
				RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, 0, started, extra, load.err)
				return nil, load.err
			}
			RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, int64(len(load.data[startIndex])), started, extra, nil)
			return load.data[startIndex], nil
		case <-ctx.Done():
			RecordChunkDetail(r.observer, ctx, entry, "wait_window_load", startIndex, 0, ChunkSize, 0, started, map[string]any{"window_start": startIndex, "window_end": endIndex}, ctx.Err())
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
		WaitFor:     key,
	})
	releaseSlot, slotErr := r.acquireReadSlot(ctx)
	if slotErr != nil {
		load.err = slotErr
		r.observer.DebugFinishActive(activeID)
		close(load.done)
		r.state.endWindowLoad(key)
		return nil, slotErr
	}
	r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
	load.data, load.extra, load.err = r.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	releaseSlot()
	r.observer.DebugFinishActive(activeID)
	RecordChunkDetail(r.observer, ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, ExtraWithWindow(load.extra, startIndex, endIndex), load.err)
	close(load.done)

	r.state.endWindowLoad(key)
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}

func (r *Reader) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	return fetchChunkWindow(ctx, entry, startIndex, endIndex, r.newBackend())
}

func fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, backend readWindowBackend) (map[int64][]byte, map[string]any, error) {
	offset := startIndex * ChunkSize
	size := (endIndex - startIndex + 1) * ChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil, nil
	}
	openStarted := util.Now()
	rc, err := backend.Read(ctx, entry, offset, size)
	openFinished := util.Now()
	extra := map[string]any{"driver_open_ms": DurationMillis(openFinished.Sub(openStarted))}
	if err != nil {
		return nil, extra, err
	}
	bodyStarted := util.Now()
	data, err := ReadAllLimited(rc, size)
	bodyFinished := util.Now()
	extra["driver_body_ms"] = DurationMillis(bodyFinished.Sub(bodyStarted))
	closeStarted := util.Now()
	closeErr := rc.Close()
	closeFinished := util.Now()
	extra["driver_close_ms"] = DurationMillis(closeFinished.Sub(closeStarted))
	if err != nil {
		return nil, extra, err
	}
	if closeErr != nil {
		return nil, extra, closeErr
	}
	chunks := make(map[int64][]byte, size/ChunkSize+1)
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
		backend.StoreChunk(backend.CacheKey(entry), entry, index, chunk)
		remaining = remaining[chunkSize:]
	}
	return chunks, extra, nil
}

func (r *Reader) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
	if fid == "" {
		return nil, false, nil
	}
	load := r.state.findWindow(fid, index)
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
		WaitFor:     WindowKey(load.fid, load.start, load.end),
	})
	defer r.observer.DebugFinishActive(activeID)
	select {
	case <-load.done:
		if load.err != nil {
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
	if _, ok := r.hotChunk(cacheKey, index); ok {
		return true
	}
	if r.state.cache != nil {
		if ok, err := r.state.cache.HasChunk(cacheKey, index); err != nil || ok {
			return true
		}
	}
	return r.state.windowContains(cacheKey, index)
}

func (r *Reader) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	return r.state.hotChunk(cacheKey, index)
}

func (r *Reader) putHotChunk(cacheKey string, index int64, data []byte) {
	r.state.putHotChunk(cacheKey, index, data)
}

func (r *Reader) acquireReadSlot(ctx context.Context) (func(), error) {
	prio := priority(ctx)
	if prio == PriorityHigh {
		select {
		case r.state.slots.normal <- struct{}{}:
			return func() { <-r.state.slots.normal }, nil
		default:
		}
		select {
		case r.state.slots.high <- struct{}{}:
			return func() { <-r.state.slots.high }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if prio == PriorityLow {
		select {
		case r.state.slots.normal <- struct{}{}:
			return func() { <-r.state.slots.normal }, nil
		default:
			return nil, fmt.Errorf("vfs: read slots full")
		}
	}
	select {
	case r.state.slots.normal <- struct{}{}:
		return func() { <-r.state.slots.normal }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
		windows = PrefetchLimit
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
	plannedEndIndex := startIndex + int64(count) - 1
	for startIndex <= plannedEndIndex {
		if entry.Size > 0 && startIndex*ChunkSize >= entry.Size {
			return
		}
		if r.readChunkAvailable(cacheKey, startIndex) {
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
		if endIndex > startIndex && r.readChunkAvailable(cacheKey, endIndex) {
			break
		}
		endIndex++
	}
	endIndex--
	if endIndex < startIndex {
		return
	}
	key := WindowKey(cacheKey, startIndex, endIndex)
	if !r.state.reservePrefetch(key) {
		return
	}
	prefetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	prefetchCtx = WithPriority(prefetchCtx, PriorityLow)

	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	if _, exists := r.state.beginWindowLoad(key, load); exists {
		r.state.releasePrefetch(key)
		cancel()
		return
	}

	go func() {
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
			WaitFor:     key,
		})
		releaseSlot, err := r.acquireReadSlot(prefetchCtx)
		if err != nil {
			load.err = err
			RecordChunkDetail(r.observer, prefetchCtx, entry, "prefetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, 0, started, nil, err)
			r.observer.DebugFinishActive(activeID)
			close(load.done)
			r.state.endWindowLoad(key)
			r.state.releasePrefetch(key)
			cancel()
			return
		}
		defer func() {
			releaseSlot()
			r.observer.DebugFinishActive(activeID)
			close(load.done)
			r.state.endWindowLoad(key)
			r.state.releasePrefetch(key)
			cancel()
		}()
		r.observer.DebugUpdateActive(activeID, func(op *vfstypes.DebugActiveOp) { op.Phase = "fetch_window" })
		load.data, load.extra, load.err = r.fetchChunkWindow(prefetchCtx, entry, startIndex, endIndex)
		RecordChunkDetail(r.observer, prefetchCtx, entry, "prefetch_window", startIndex, 0, (endIndex-startIndex+1)*ChunkSize, WindowBytes(load.data), started, load.extra, load.err)
	}()
}
