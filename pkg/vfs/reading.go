package vfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type debugReadCloser struct {
	io.ReadCloser
	mu     sync.Mutex
	bytes  int64
	err    error
	once   sync.Once
	finish func(int64, error)
}

func (r *debugReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.mu.Lock()
	r.bytes += int64(n)
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	return n, err
}

func (r *debugReadCloser) Close() error {
	closeErr := r.ReadCloser.Close()
	r.mu.Lock()
	if closeErr != nil && r.err == nil {
		r.err = closeErr
	}
	bytes, err := r.bytes, r.err
	r.mu.Unlock()
	r.once.Do(func() {
		if r.finish != nil {
			r.finish(bytes, err)
		}
	})
	return closeErr
}

const readChunkSize = 1024 * 1024
const readPrefetchLimit = 2
const readPrefetchChunks = 8
const readHotChunkLimit = 16
const readRangeHitLimit = 1024
const readRangePromoteHits = 2

func readWindowExtra(windowChunks int) map[string]any {
	return map[string]any{
		"window_chunks": windowChunks,
	}
}

func (v *VFS) readRange(ctx context.Context, entry drive.Entry, offset, size int64, windowChunks int) ([]byte, int64, int64, error) {
	if offset < 0 || size < 0 {
		return nil, 0, 0, fmt.Errorf("vfs: read offset and size must be non-negative")
	}
	startChunk := offset / readChunkSize
	endChunk := startChunk
	if entry.Size > 0 && offset >= entry.Size {
		return nil, startChunk, endChunk, nil
	}
	var out bytes.Buffer
	if size > 0 && size <= readChunkSize {
		out.Grow(int(size))
	}
	pos := offset
	end, endKnown := readEnd(offset, size, entry.Size)
	for {
		if endKnown && pos >= end {
			break
		}
		chunkIndex := pos / readChunkSize
		chunkStart := chunkIndex * readChunkSize
		start := pos - chunkStart
		want := int64(readChunkSize) - start
		if endKnown && end-pos < want {
			want = end - pos
		}
		chunk, err := v.readChunkRange(ctx, entry, chunkIndex, start, want, windowChunks)
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

func (v *VFS) readChunkRange(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int) ([]byte, error) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey != "" {
		hotStarted := timeutil.Now()
		if hot, ok := v.hotChunk(cacheKey, index); ok {
			v.cache.addHit()
			data := sliceChunkRange(hot, start, size)
			v.recordReadChunkDetail(ctx, entry, "cache_hot_hit", index, start, size, int64(len(data)), hotStarted, readCacheLookupExtra("hot", hotStarted, nil), nil)
			return data, nil
		}
		if shouldPromoteCachedRange(size) && v.shouldPromoteCachedRange(cacheKey, index) {
			started := timeutil.Now()
			if cached, chunk, ok, err := v.cache.GetChunkWithRange(cacheKey, index, start, size); err != nil {
				v.recordReadChunkDetail(ctx, entry, "cache_range_promote", index, start, size, 0, started, readCacheLookupExtra("range_promote", started, nil), err)
				return nil, err
			} else if ok {
				if len(chunk) > 0 {
					v.putHotChunk(cacheKey, index, chunk)
				}
				v.recordReadChunkDetail(ctx, entry, "cache_range_promote", index, start, size, int64(len(cached)), started, readCacheLookupExtra("range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
				return cached, nil
			}
		}
		started := timeutil.Now()
		if cached, ok, err := v.cache.GetChunkRange(cacheKey, index, start, size); err != nil {
			v.recordReadChunkDetail(ctx, entry, "cache_range_hit", index, start, size, 0, started, readCacheLookupExtra("range", started, nil), err)
			return nil, err
		} else if ok {
			v.recordCachedRangeHit(cacheKey, index, size)
			v.recordReadChunkDetail(ctx, entry, "cache_range_hit", index, start, size, int64(len(cached)), started, readCacheLookupExtra("range", started, nil), nil)
			return cached, nil
		}
	}
	waitStarted := timeutil.Now()
	if data, ok, err := v.waitWindow(ctx, cacheKey, index); err != nil {
		v.recordReadChunkDetail(ctx, entry, "wait_window", index, start, size, 0, waitStarted, nil, err)
		return nil, err
	} else if ok {
		if data != nil {
			chunk := sliceChunkRange(data, start, size)
			v.recordReadChunkDetail(ctx, entry, "wait_window", index, start, size, int64(len(chunk)), waitStarted, nil, nil)
			return chunk, nil
		}
		if cacheKey != "" {
			if shouldPromoteCachedRange(size) && v.shouldPromoteCachedRange(cacheKey, index) {
				started := timeutil.Now()
				if cached, chunk, ok, err := v.cache.GetChunkWithRange(cacheKey, index, start, size); err != nil {
					v.recordReadChunkDetail(ctx, entry, "wait_window_cache_promote", index, start, size, 0, started, readCacheLookupExtra("wait_window_range_promote", started, nil), err)
					return nil, err
				} else if ok {
					if len(chunk) > 0 {
						v.putHotChunk(cacheKey, index, chunk)
					}
					v.recordReadChunkDetail(ctx, entry, "wait_window_cache_promote", index, start, size, int64(len(cached)), started, readCacheLookupExtra("wait_window_range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
					return cached, nil
				}
			}
			started := timeutil.Now()
			if cached, ok, err := v.cache.GetChunkRange(cacheKey, index, start, size); err != nil {
				v.recordReadChunkDetail(ctx, entry, "wait_window_cache_hit", index, start, size, 0, started, readCacheLookupExtra("wait_window_range", started, nil), err)
				return nil, err
			} else if ok {
				v.recordCachedRangeHit(cacheKey, index, size)
				v.recordReadChunkDetail(ctx, entry, "wait_window_cache_hit", index, start, size, int64(len(cached)), started, readCacheLookupExtra("wait_window_range", started, nil), nil)
				return cached, nil
			}
		}
	}
	var data []byte
	var err error
	loadStarted := timeutil.Now()
	data, err = v.loadWindow(ctx, entry, index, windowChunks)
	if err != nil {
		v.recordReadChunkDetail(ctx, entry, "cache_miss_load", index, start, size, 0, loadStarted, nil, err)
		return nil, err
	}
	chunk := sliceChunkRange(data, start, size)
	v.recordReadChunkDetail(ctx, entry, "cache_miss_load", index, start, size, int64(len(chunk)), loadStarted, readWindowExtra(windowChunks), nil)
	return chunk, nil
}

func readCacheLookupExtra(source string, started time.Time, extra map[string]any) map[string]any {
	out := cloneReadExtra(extra)
	if out == nil {
		out = map[string]any{}
	}
	out["cache_lookup_source"] = source
	out["cache_lookup_ms"] = durationMillis(timeutil.Now().Sub(started))
	return out
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

func readWindowChunks(requestSize int64) int {
	if requestSize > 0 && requestSize <= readChunkSize {
		return 1
	}
	return readPrefetchChunks
}

func shouldPromoteCachedRange(requestSize int64) bool {
	return requestSize > 0 && requestSize < readChunkSize
}

func (v *VFS) recordCachedRangeHit(cacheKey string, index, requestSize int64) {
	if !shouldPromoteCachedRange(requestSize) {
		return
	}
	key := readChunkKey(cacheKey, index)
	v.rangeHitMu.Lock()
	defer v.rangeHitMu.Unlock()
	if _, ok := v.rangeHits[key]; !ok {
		v.rangeHitLRU = append(v.rangeHitLRU, key)
	}
	v.rangeHits[key]++
	for len(v.rangeHitLRU) > readRangeHitLimit {
		oldest := v.rangeHitLRU[0]
		v.rangeHitLRU = v.rangeHitLRU[1:]
		delete(v.rangeHits, oldest)
	}
}

func (v *VFS) shouldPromoteCachedRange(cacheKey string, index int64) bool {
	key := readChunkKey(cacheKey, index)
	v.rangeHitMu.Lock()
	defer v.rangeHitMu.Unlock()
	hits := v.rangeHits[key]
	if hits+1 < readRangePromoteHits {
		return false
	}
	delete(v.rangeHits, key)
	for i, candidate := range v.rangeHitLRU {
		if candidate == key {
			v.rangeHitLRU = append(v.rangeHitLRU[:i], v.rangeHitLRU[i+1:]...)
			break
		}
	}
	return true
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
	v.windowLoadMu.Lock()
	if load := v.windowLoads[key]; load != nil {
		v.windowLoadMu.Unlock()
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
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	v.windowLoads[key] = load
	v.windowLoadMu.Unlock()

	started := timeutil.Now()
	activeID := v.beginDebugActive(DebugActiveOp{
		Kind:        "vfs_window_load",
		Phase:       "fetch_window",
		Path:        debugOperationName(ctx),
		RemoteID:    entry.ID,
		Offset:      startIndex * readChunkSize,
		Requested:   (endIndex - startIndex + 1) * readChunkSize,
		ChunkIndex:  startIndex,
		WindowStart: startIndex,
		WindowEnd:   endIndex,
		WaitFor:     key,
	})
	load.data, load.extra, load.err = v.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	v.finishDebugActive(activeID)
	v.recordReadChunkDetail(ctx, entry, "fetch_window", startIndex, 0, (endIndex-startIndex+1)*readChunkSize, windowBytes(load.data), started, readExtraWithWindow(load.extra, startIndex, endIndex), load.err)
	close(load.done)

	v.windowLoadMu.Lock()
	delete(v.windowLoads, key)
	v.windowLoadMu.Unlock()
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}

func (v *VFS) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int) {
	if windowChunks <= 0 {
		return
	}
	v.prefetchWindow(ctx, entry, endChunk+1, windowChunks)
}

type windowLoad struct {
	fid   string
	start int64
	end   int64
	done  chan struct{}
	data  map[int64][]byte
	extra map[string]any
	err   error
}

func (v *VFS) prefetchWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) {
	if startIndex < 0 || count <= 0 {
		return
	}
	if entry.Size > 0 && startIndex*readChunkSize >= entry.Size {
		return
	}
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" {
		return
	}
	maxEndIndex := startIndex + int64(count) - 1
	for startIndex <= maxEndIndex {
		if entry.Size > 0 && startIndex*readChunkSize >= entry.Size {
			return
		}
		if v.readChunkAvailable(cacheKey, startIndex) {
			startIndex++
			continue
		}
		break
	}
	endIndex := startIndex
	for endIndex <= maxEndIndex {
		if entry.Size > 0 && endIndex*readChunkSize >= entry.Size {
			break
		}
		if endIndex > startIndex && v.readChunkAvailable(cacheKey, endIndex) {
			break
		}
		endIndex++
	}
	endIndex--
	if endIndex < startIndex {
		return
	}
	key := readWindowKey(cacheKey, startIndex, endIndex)
	v.prefetchMu.Lock()
	if _, ok := v.prefetching[key]; ok {
		v.prefetchMu.Unlock()
		return
	}
	v.prefetching[key] = struct{}{}
	v.prefetchMu.Unlock()
	select {
	case v.prefetchSem <- struct{}{}:
	default:
		v.prefetchMu.Lock()
		delete(v.prefetching, key)
		v.prefetchMu.Unlock()
		return
	}
	prefetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	v.windowLoadMu.Lock()
	v.windowLoads[key] = load
	v.windowLoadMu.Unlock()

	go func() {
		activeID := v.beginDebugActive(DebugActiveOp{
			Kind:        "vfs_prefetch",
			Phase:       "fetch_window",
			Path:        debugOperationName(ctx),
			RemoteID:    entry.ID,
			Offset:      startIndex * readChunkSize,
			Requested:   (endIndex - startIndex + 1) * readChunkSize,
			ChunkIndex:  startIndex,
			WindowStart: startIndex,
			WindowEnd:   endIndex,
			Background:  true,
			WaitFor:     key,
		})
		defer func() {
			v.finishDebugActive(activeID)
			close(load.done)
			v.windowLoadMu.Lock()
			delete(v.windowLoads, key)
			v.windowLoadMu.Unlock()
			<-v.prefetchSem
			v.prefetchMu.Lock()
			delete(v.prefetching, key)
			v.prefetchMu.Unlock()
			cancel()
		}()
		load.data, load.extra, load.err = v.fetchChunkWindow(prefetchCtx, entry, startIndex, endIndex)
	}()
}

func (v *VFS) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, map[string]any, error) {
	offset := startIndex * readChunkSize
	size := (endIndex - startIndex + 1) * readChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil, nil
	}
	openStarted := timeutil.Now()
	rc, err := v.driver.Read(ctx, entry, offset, size)
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
	chunks := map[int64][]byte{}
	for index := startIndex; len(data) > 0 && index <= endIndex; index++ {
		chunkSize := readChunkSize
		if len(data) < chunkSize {
			chunkSize = len(data)
		}
		chunk := make([]byte, chunkSize)
		copy(chunk, data[:chunkSize])
		chunks[index] = chunk
		if cacheKey := v.readCacheKey(entry); cacheKey != "" {
			v.putHotChunk(cacheKey, index, chunk)
			v.cache.PutChunkAsync(cacheKey, entry.Size, index, chunk)
		}
		data = data[chunkSize:]
	}
	return chunks, extra, nil
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
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

func (v *VFS) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
	if fid == "" {
		return nil, false, nil
	}
	v.windowLoadMu.Lock()
	var load *windowLoad
	for _, candidate := range v.windowLoads {
		if candidate.fid == fid && index >= candidate.start && index <= candidate.end {
			load = candidate
			break
		}
	}
	v.windowLoadMu.Unlock()
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
	if _, ok := v.hotChunk(cacheKey, index); ok {
		return true
	}
	if ok, err := v.cache.HasChunk(cacheKey, index); err != nil || ok {
		return true
	}
	v.windowLoadMu.Lock()
	defer v.windowLoadMu.Unlock()
	for _, load := range v.windowLoads {
		if load.fid == cacheKey && index >= load.start && index <= load.end {
			return true
		}
	}
	return false
}

func (v *VFS) recordReadChunkDetail(ctx context.Context, entry drive.Entry, phase string, index, start, size, bytes int64, started time.Time, extra map[string]any, err error) {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok || op.OpID == "" {
		return
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["chunk_index"] = index
	extra["chunk_offset"] = index * readChunkSize
	extra["chunk_range_start"] = start
	extra["chunk_range_size"] = size
	v.recordDebugReadDetail(ctx, op.Name, entry.ID, phase, index*readChunkSize+start, size, bytes, started, extra, err)
}

func debugOperationName(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Name
}

func debugOperationStep(ctx context.Context) string {
	op, ok := drive.DebugOperationFromContext(ctx)
	if !ok {
		return ""
	}
	return op.Step
}

func windowBytes(chunks map[int64][]byte) int64 {
	var total int64
	for _, chunk := range chunks {
		total += int64(len(chunk))
	}
	return total
}

func cloneReadExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	clone := make(map[string]any, len(extra))
	for key, value := range extra {
		clone[key] = value
	}
	return clone
}

func readExtraWithWindow(extra map[string]any, start, end int64) map[string]any {
	merged := cloneReadExtra(extra)
	if merged == nil {
		merged = map[string]any{}
	}
	merged["window_start"] = start
	merged["window_end"] = end
	return merged
}

func (v *VFS) hotChunk(cacheKey string, index int64) ([]byte, bool) {
	key := readChunkKey(cacheKey, index)
	v.hotChunkMu.Lock()
	defer v.hotChunkMu.Unlock()
	data, ok := v.hotChunks[key]
	if !ok {
		return nil, false
	}
	for i, candidate := range v.hotChunkLRU {
		if candidate == key {
			copy(v.hotChunkLRU[i:], v.hotChunkLRU[i+1:])
			v.hotChunkLRU[len(v.hotChunkLRU)-1] = key
			break
		}
	}
	return data, true
}

func (v *VFS) putHotChunk(cacheKey string, index int64, data []byte) {
	key := readChunkKey(cacheKey, index)
	v.hotChunkMu.Lock()
	defer v.hotChunkMu.Unlock()
	if _, ok := v.hotChunks[key]; !ok {
		v.hotChunkLRU = append(v.hotChunkLRU, key)
	}
	v.hotChunks[key] = data
	for len(v.hotChunkLRU) > readHotChunkLimit {
		oldest := v.hotChunkLRU[0]
		v.hotChunkLRU = v.hotChunkLRU[1:]
		delete(v.hotChunks, oldest)
	}
}

func (v *VFS) debugHotChunks() (int, int64) {
	v.hotChunkMu.Lock()
	defer v.hotChunkMu.Unlock()
	var bytes int64
	for _, data := range v.hotChunks {
		bytes += int64(len(data))
	}
	return len(v.hotChunks), bytes
}

func (v *VFS) readLoadKey(entry drive.Entry) string {
	if key := v.readCacheKey(entry); key != "" {
		return key
	}
	return entry.ID
}

func (v *VFS) readCacheKey(entry drive.Entry) string {
	if entry.ID == "" || entry.ModTime.IsZero() {
		return ""
	}
	sum := sha256.Sum256([]byte(v.rootID + "\x00" + entry.ID + "\x00" + strconv.FormatInt(entry.Size, 10) + "\x00" + strconv.FormatInt(entry.ModTime.UTC().UnixNano(), 10)))
	return hex.EncodeToString(sum[:])
}

func readChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

func readWindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}
