package vfs

import (
	"bytes"
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/internal/timeutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"io"
)

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
	runtime := newVFSReadRuntime(v)
	return v.readChunkRangeWithRuntime(ctx, entry, index, start, size, windowChunks, runtime)
}

func (v *VFS) readChunkRangeWithRuntime(ctx context.Context, entry drive.Entry, index, start, size int64, windowChunks int, runtime readRuntime) ([]byte, error) {
	cacheKey := runtime.CacheKey(entry)
	if cacheKey != "" {
		hotStarted := timeutil.Now()
		if hot, ok := runtime.HotChunk(cacheKey, index); ok {
			runtime.AddCacheHit()
			data := sliceChunkRange(hot, start, size)
			v.recordReadChunkDetail(ctx, entry, "cache_hot_hit", index, start, size, int64(len(data)), hotStarted, readCacheLookupExtra("hot", hotStarted, nil), nil)
			return data, nil
		}
		if shouldPromoteCachedRange(size) && runtime.ShouldPromoteCachedRange(cacheKey, index) {
			started := timeutil.Now()
			if cached, chunk, ok, err := runtime.GetChunkWithRange(cacheKey, index, start, size); err != nil {
				v.recordReadChunkDetail(ctx, entry, "cache_range_promote", index, start, size, 0, started, readCacheLookupExtra("range_promote", started, nil), err)
				return nil, err
			} else if ok {
				if len(chunk) > 0 {
					runtime.PutHotChunk(cacheKey, index, chunk)
				}
				v.recordReadChunkDetail(ctx, entry, "cache_range_promote", index, start, size, int64(len(cached)), started, readCacheLookupExtra("range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
				return cached, nil
			}
		}
		started := timeutil.Now()
		if cached, ok, err := runtime.GetChunkRange(cacheKey, index, start, size); err != nil {
			v.recordReadChunkDetail(ctx, entry, "cache_range_hit", index, start, size, 0, started, readCacheLookupExtra("range", started, nil), err)
			return nil, err
		} else if ok {
			runtime.RecordCachedRangeHit(cacheKey, index, size)
			v.recordReadChunkDetail(ctx, entry, "cache_range_hit", index, start, size, int64(len(cached)), started, readCacheLookupExtra("range", started, nil), nil)
			return cached, nil
		}
	}
	waitStarted := timeutil.Now()
	if data, ok, err := runtime.WaitWindow(ctx, cacheKey, index); err != nil {
		v.recordReadChunkDetail(ctx, entry, "wait_window", index, start, size, 0, waitStarted, nil, err)
		return nil, err
	} else if ok {
		if data != nil {
			chunk := sliceChunkRange(data, start, size)
			v.recordReadChunkDetail(ctx, entry, "wait_window", index, start, size, int64(len(chunk)), waitStarted, nil, nil)
			return chunk, nil
		}
		if cacheKey != "" {
			if shouldPromoteCachedRange(size) && runtime.ShouldPromoteCachedRange(cacheKey, index) {
				started := timeutil.Now()
				if cached, chunk, ok, err := runtime.GetChunkWithRange(cacheKey, index, start, size); err != nil {
					v.recordReadChunkDetail(ctx, entry, "wait_window_cache_promote", index, start, size, 0, started, readCacheLookupExtra("wait_window_range_promote", started, nil), err)
					return nil, err
				} else if ok {
					if len(chunk) > 0 {
						runtime.PutHotChunk(cacheKey, index, chunk)
					}
					v.recordReadChunkDetail(ctx, entry, "wait_window_cache_promote", index, start, size, int64(len(cached)), started, readCacheLookupExtra("wait_window_range_promote", started, map[string]any{"promoted": len(chunk) > 0}), nil)
					return cached, nil
				}
			}
			started := timeutil.Now()
			if cached, ok, err := runtime.GetChunkRange(cacheKey, index, start, size); err != nil {
				v.recordReadChunkDetail(ctx, entry, "wait_window_cache_hit", index, start, size, 0, started, readCacheLookupExtra("wait_window_range", started, nil), err)
				return nil, err
			} else if ok {
				runtime.RecordCachedRangeHit(cacheKey, index, size)
				v.recordReadChunkDetail(ctx, entry, "wait_window_cache_hit", index, start, size, int64(len(cached)), started, readCacheLookupExtra("wait_window_range", started, nil), nil)
				return cached, nil
			}
		}
	}
	var data []byte
	var err error
	loadStarted := timeutil.Now()
	data, err = runtime.LoadWindow(ctx, entry, index, windowChunks)
	if err != nil {
		v.recordReadChunkDetail(ctx, entry, "cache_miss_load", index, start, size, 0, loadStarted, nil, err)
		return nil, err
	}
	chunk := sliceChunkRange(data, start, size)
	v.recordReadChunkDetail(ctx, entry, "cache_miss_load", index, start, size, int64(len(chunk)), loadStarted, readWindowExtra(windowChunks), nil)
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
func readWindowChunks(requestSize int64) int {
	if requestSize > 0 && requestSize <= readChunkSize {
		return 1
	}
	return readPrefetchChunks
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
