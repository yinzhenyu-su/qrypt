package vfs

import (
	"context"
	"errors"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
)

type windowLoad struct {
	fid   string
	start int64
	end   int64
	done  chan struct{}
	data  map[int64][]byte
	extra map[string]any
	err   error
}

func (v *VFS) loadWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error) {
	if count <= 0 {
		count = 1
	}
	cacheKey := v.readCacheKey(entry)
	endIndex := startIndex + int64(count) - 1
	if entry.Size > 0 {
		lastIndex := (entry.Size - 1) / readChunkSize
		if endIndex > lastIndex {
			endIndex = lastIndex
		}
	}
	if cacheKey != "" {
		for endIndex > startIndex && v.readChunkAvailable(cacheKey, endIndex) {
			endIndex--
		}
	}
	key := readWindowKey(v.readLoadKey(entry), startIndex, endIndex)
	runtime := newVFSReadRuntime(v)
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	if load, ok := runtime.BeginWindowLoad(key, load); ok {
		started := timeutil.Now()
		activeID := v.beginDebugActive(DebugActiveOp{
			Kind:        "vfs_wait",
			Phase:       "wait_window_load",
			Path:        debugOperationName(ctx),
			RemoteID:    entry.ID,
			ChunkIndex:  startIndex,
			WindowStart: startIndex,
			WindowEnd:   endIndex,
			WaitFor:     key,
		})
		defer v.finishDebugActive(activeID)
		select {
		case <-load.done:
			extra := readExtraWithWindow(load.extra, load.start, load.end)
			if load.err != nil {
				v.recordReadChunkDetail(ctx, entry, "wait_window_load", startIndex, 0, readChunkSize, 0, started, extra, load.err)
				return nil, load.err
			}
			v.recordReadChunkDetail(ctx, entry, "wait_window_load", startIndex, 0, readChunkSize, int64(len(load.data[startIndex])), started, extra, nil)
			return load.data[startIndex], nil
		case <-ctx.Done():
			v.recordReadChunkDetail(ctx, entry, "wait_window_load", startIndex, 0, readChunkSize, 0, started, map[string]any{"window_start": startIndex, "window_end": endIndex}, ctx.Err())
			return nil, ctx.Err()
		}
	}

	started := timeutil.Now()
	activeID := v.beginDebugActive(DebugActiveOp{
		Kind:        "vfs_window_load",
		Phase:       "acquire_slot",
		Path:        debugOperationName(ctx),
		RemoteID:    entry.ID,
		Offset:      startIndex * readChunkSize,
		Requested:   (endIndex - startIndex + 1) * readChunkSize,
		ChunkIndex:  startIndex,
		WindowStart: startIndex,
		WindowEnd:   endIndex,
		WaitFor:     key,
	})
	releaseSlot, slotErr := v.acquireReadSlot(ctx)
	if slotErr != nil {
		load.err = slotErr
		v.finishDebugActive(activeID)
		close(load.done)
		runtime.EndWindowLoad(key)
		return nil, slotErr
	}
	v.updateDebugActive(activeID, func(op *DebugActiveOp) { op.Phase = "fetch_window" })
	load.data, load.extra, load.err = v.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	releaseSlot()
	v.finishDebugActive(activeID)
	v.recordReadChunkDetail(ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*readChunkSize, windowBytes(load.data), started, readExtraWithWindow(load.extra, startIndex, endIndex), load.err)
	close(load.done)

	runtime.EndWindowLoad(key)
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}
func (v *VFS) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	return fetchChunkWindow(ctx, entry, startIndex, endIndex, newVFSReadWindowBackend(v))
}

func fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64, backend readWindowBackend) (map[int64][]byte, map[string]any, error) {
	offset := startIndex * readChunkSize
	size := (endIndex - startIndex + 1) * readChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil, nil
	}
	openStarted := timeutil.Now()
	rc, err := backend.Read(ctx, entry, offset, size)
	openFinished := timeutil.Now()
	extra := map[string]any{"driver_open_ms": durationMillis(openFinished.Sub(openStarted))}
	if err != nil {
		return nil, extra, err
	}
	bodyStarted := timeutil.Now()
	data, err := readAllLimited(rc, size)
	bodyFinished := timeutil.Now()
	extra["driver_body_ms"] = durationMillis(bodyFinished.Sub(bodyStarted))
	closeStarted := timeutil.Now()
	closeErr := rc.Close()
	closeFinished := timeutil.Now()
	extra["driver_close_ms"] = durationMillis(closeFinished.Sub(closeStarted))
	if err != nil {
		return nil, extra, err
	}
	if closeErr != nil {
		return nil, extra, closeErr
	}
	chunks := make(map[int64][]byte, size/readChunkSize+1)
	remaining := data
	for index := startIndex; len(remaining) > 0 && index <= endIndex; index++ {
		chunkSize := readChunkSize
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
func (v *VFS) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
	if fid == "" {
		return nil, false, nil
	}
	load := newVFSReadRuntime(v).FindWindow(fid, index)
	if load == nil {
		return nil, false, nil
	}
	activeID := v.beginDebugActive(DebugActiveOp{
		Kind:        "vfs_wait",
		Phase:       "wait_window",
		Path:        debugOperationName(ctx),
		RemoteID:    fid,
		ChunkIndex:  index,
		WindowStart: load.start,
		WindowEnd:   load.end,
		WaitFor:     readWindowKey(load.fid, load.start, load.end),
	})
	defer v.finishDebugActive(activeID)
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
func (v *VFS) readChunkAvailable(cacheKey string, index int64) bool {
	return newVFSReadRuntime(v).ChunkAvailable(cacheKey, index)
}
func windowBytes(chunks map[int64][]byte) int64 {
	var total int64
	for _, chunk := range chunks {
		total += int64(len(chunk))
	}
	return total
}

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

type vfsReadRuntime struct {
	v *VFS
}

func newVFSReadRuntime(v *VFS) vfsReadRuntime {
	return vfsReadRuntime{v: v}
}

func (r vfsReadRuntime) CacheKey(entry drive.Entry) string {
	return r.v.readCacheKey(entry)
}

func (r vfsReadRuntime) AddCacheHit() {
	r.v.readCache.addHit()
}

func (r vfsReadRuntime) HotChunk(cacheKey string, index int64) ([]byte, bool) {
	key := readChunkKey(cacheKey, index)
	r.v.readFastPath.hot.mu.Lock()
	defer r.v.readFastPath.hot.mu.Unlock()
	data, ok := r.v.readFastPath.hot.chunks[key]
	if !ok {
		return nil, false
	}
	for i, candidate := range r.v.readFastPath.hot.lru {
		if candidate == key {
			copy(r.v.readFastPath.hot.lru[i:], r.v.readFastPath.hot.lru[i+1:])
			r.v.readFastPath.hot.lru[len(r.v.readFastPath.hot.lru)-1] = key
			break
		}
	}
	return data, true
}

func (r vfsReadRuntime) PutHotChunk(cacheKey string, index int64, data []byte) {
	key := readChunkKey(cacheKey, index)
	r.v.readFastPath.hot.mu.Lock()
	defer r.v.readFastPath.hot.mu.Unlock()
	if _, ok := r.v.readFastPath.hot.chunks[key]; !ok {
		r.v.readFastPath.hot.lru = append(r.v.readFastPath.hot.lru, key)
	}
	r.v.readFastPath.hot.chunks[key] = data
	for len(r.v.readFastPath.hot.lru) > readHotChunkLimit {
		oldest := r.v.readFastPath.hot.lru[0]
		r.v.readFastPath.hot.lru = r.v.readFastPath.hot.lru[1:]
		delete(r.v.readFastPath.hot.chunks, oldest)
	}
}

func (r vfsReadRuntime) ShouldPromoteCachedRange(cacheKey string, index int64) bool {
	key := readChunkKey(cacheKey, index)
	r.v.readFastPath.rangeHit.mu.Lock()
	defer r.v.readFastPath.rangeHit.mu.Unlock()
	hits := r.v.readFastPath.rangeHit.hits[key]
	if hits+1 < readRangePromoteHits {
		return false
	}
	delete(r.v.readFastPath.rangeHit.hits, key)
	for i, candidate := range r.v.readFastPath.rangeHit.lru {
		if candidate == key {
			r.v.readFastPath.rangeHit.lru = append(r.v.readFastPath.rangeHit.lru[:i], r.v.readFastPath.rangeHit.lru[i+1:]...)
			break
		}
	}
	return true
}

func (r vfsReadRuntime) RecordCachedRangeHit(cacheKey string, index, requestSize int64) {
	if !shouldPromoteCachedRange(requestSize) {
		return
	}
	key := readChunkKey(cacheKey, index)
	r.v.readFastPath.rangeHit.mu.Lock()
	defer r.v.readFastPath.rangeHit.mu.Unlock()
	if _, ok := r.v.readFastPath.rangeHit.hits[key]; !ok {
		r.v.readFastPath.rangeHit.lru = append(r.v.readFastPath.rangeHit.lru, key)
	}
	r.v.readFastPath.rangeHit.hits[key]++
	for len(r.v.readFastPath.rangeHit.lru) > readRangeHitLimit {
		oldest := r.v.readFastPath.rangeHit.lru[0]
		r.v.readFastPath.rangeHit.lru = r.v.readFastPath.rangeHit.lru[1:]
		delete(r.v.readFastPath.rangeHit.hits, oldest)
	}
}

func (r vfsReadRuntime) FlushStaging(localPath string) error {
	return r.v.uploads.staging.flush(localPath)
}

func (r vfsReadRuntime) ChunkAvailable(cacheKey string, index int64) bool {
	if _, ok := r.v.hotChunk(cacheKey, index); ok {
		return true
	}
	if ok, err := r.v.readCache.HasChunk(cacheKey, index); err != nil || ok {
		return true
	}
	return r.WindowContains(cacheKey, index)
}

func (r vfsReadRuntime) GetChunkWithRange(cacheKey string, index, start, size int64) ([]byte, []byte, bool, error) {
	return r.v.readCache.GetChunkWithRange(cacheKey, index, start, size)
}

func (r vfsReadRuntime) GetChunkRange(cacheKey string, index, start, size int64) ([]byte, bool, error) {
	return r.v.readCache.GetChunkRange(cacheKey, index, start, size)
}

func (r vfsReadRuntime) WaitWindow(ctx context.Context, cacheKey string, index int64) ([]byte, bool, error) {
	return r.v.waitWindow(ctx, cacheKey, index)
}

func (r vfsReadRuntime) LoadWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error) {
	return r.v.loadWindow(ctx, entry, startIndex, count)
}

func (r vfsReadRuntime) AcquireSlot(ctx context.Context) (func(), error) {
	prio := readPriority(ctx)
	if prio == PriorityHigh {
		select {
		case r.v.readSlots.normal <- struct{}{}:
			return func() { <-r.v.readSlots.normal }, nil
		default:
		}
		select {
		case r.v.readSlots.high <- struct{}{}:
			return func() { <-r.v.readSlots.high }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if prio == PriorityLow {
		select {
		case r.v.readSlots.normal <- struct{}{}:
			return func() { <-r.v.readSlots.normal }, nil
		default:
			return nil, fmt.Errorf("vfs: read slots full")
		}
	}
	select {
	case r.v.readSlots.normal <- struct{}{}:
		return func() { <-r.v.readSlots.normal }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r vfsReadRuntime) BeginWindowLoad(key string, load *windowLoad) (*windowLoad, bool) {
	r.v.readWindows.mu.Lock()
	defer r.v.readWindows.mu.Unlock()
	if existing := r.v.readWindows.loads[key]; existing != nil {
		return existing, true
	}
	r.v.readWindows.loads[key] = load
	return load, false
}

func (r vfsReadRuntime) EndWindowLoad(key string) {
	r.v.readWindows.mu.Lock()
	delete(r.v.readWindows.loads, key)
	r.v.readWindows.mu.Unlock()
}

func (r vfsReadRuntime) FindWindow(cacheKey string, index int64) *windowLoad {
	r.v.readWindows.mu.Lock()
	defer r.v.readWindows.mu.Unlock()
	for _, candidate := range r.v.readWindows.loads {
		if candidate.fid == cacheKey && index >= candidate.start && index <= candidate.end {
			return candidate
		}
	}
	return nil
}

func (r vfsReadRuntime) WindowContains(cacheKey string, index int64) bool {
	return r.FindWindow(cacheKey, index) != nil
}

func (r vfsReadRuntime) ReservePrefetch(key string) bool {
	r.v.readPrefetch.mu.Lock()
	if _, ok := r.v.readPrefetch.inFlight[key]; ok {
		r.v.readPrefetch.mu.Unlock()
		return false
	}
	r.v.readPrefetch.inFlight[key] = struct{}{}
	r.v.readPrefetch.mu.Unlock()

	select {
	case r.v.readPrefetch.sem <- struct{}{}:
		return true
	default:
		r.v.readPrefetch.mu.Lock()
		delete(r.v.readPrefetch.inFlight, key)
		r.v.readPrefetch.mu.Unlock()
		return false
	}
}

func (r vfsReadRuntime) ReleasePrefetch(key string) {
	<-r.v.readPrefetch.sem
	r.v.readPrefetch.mu.Lock()
	delete(r.v.readPrefetch.inFlight, key)
	r.v.readPrefetch.mu.Unlock()
}

func (r vfsReadRuntime) HotChunkStats() (int, int64) {
	r.v.readFastPath.hot.mu.Lock()
	defer r.v.readFastPath.hot.mu.Unlock()
	var bytes int64
	for _, data := range r.v.readFastPath.hot.chunks {
		bytes += int64(len(data))
	}
	return len(r.v.readFastPath.hot.chunks), bytes
}

type readWindowBackend interface {
	Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error)
	CacheKey(entry drive.Entry) string
	StoreChunk(cacheKey string, entry drive.Entry, index int64, chunk []byte)
}

type vfsReadWindowBackend struct {
	v *VFS
}

func newVFSReadWindowBackend(v *VFS) vfsReadWindowBackend {
	return vfsReadWindowBackend{v: v}
}

func (b vfsReadWindowBackend) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	return b.v.driver.Read(ctx, entry, offset, size)
}

func (b vfsReadWindowBackend) CacheKey(entry drive.Entry) string {
	return b.v.readCacheKey(entry)
}

func (b vfsReadWindowBackend) StoreChunk(cacheKey string, entry drive.Entry, index int64, chunk []byte) {
	if cacheKey == "" {
		return
	}
	b.v.putHotChunk(cacheKey, index, chunk)
	b.v.readCache.PutChunkAsync(cacheKey, entry.Size, index, chunk)
}
