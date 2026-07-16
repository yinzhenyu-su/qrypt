package vfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"sync"

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

const readChunkSize = 512 * 1024
const readPrefetchRadius = 1
const readPrefetchLimit = 2
const readPrefetchChunks = 8
const readHotChunkLimit = 64
const readRangeHitLimit = 1024
const readRangePromoteHits = 2

func (v *VFS) readRange(ctx context.Context, entry drive.Entry, offset, size int64) ([]byte, int64, int64, error) {
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
		chunk, err := v.readChunkRange(ctx, entry, chunkIndex, start, want)
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

func (v *VFS) readChunkRange(ctx context.Context, entry drive.Entry, index, start, size int64) ([]byte, error) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey != "" {
		if hot, ok := v.hotChunk(cacheKey, index); ok {
			v.cache.addHit()
			return sliceChunkRange(hot, start, size), nil
		}
		if shouldPromoteCachedRange(size) && v.shouldPromoteCachedRange(cacheKey, index) {
			if cached, chunk, ok, err := v.cache.GetChunkWithRange(cacheKey, index, start, size); err != nil {
				return nil, err
			} else if ok {
				if len(chunk) > 0 {
					v.putHotChunk(cacheKey, index, chunk)
				}
				return cached, nil
			}
		}
		if cached, ok, err := v.cache.GetChunkRange(cacheKey, index, start, size); err != nil {
			return nil, err
		} else if ok {
			v.recordCachedRangeHit(cacheKey, index, size)
			return cached, nil
		}
	}
	if data, ok, err := v.waitWindow(ctx, cacheKey, index); err != nil {
		return nil, err
	} else if ok {
		if data != nil {
			return sliceChunkRange(data, start, size), nil
		}
		if cacheKey != "" {
			if shouldPromoteCachedRange(size) && v.shouldPromoteCachedRange(cacheKey, index) {
				if cached, chunk, ok, err := v.cache.GetChunkWithRange(cacheKey, index, start, size); err != nil {
					return nil, err
				} else if ok {
					if len(chunk) > 0 {
						v.putHotChunk(cacheKey, index, chunk)
					}
					return cached, nil
				}
			}
			if cached, ok, err := v.cache.GetChunkRange(cacheKey, index, start, size); err != nil {
				return nil, err
			} else if ok {
				v.recordCachedRangeHit(cacheKey, index, size)
				return cached, nil
			}
		}
	}
	var data []byte
	var err error
	if cacheKey != "" {
		data, err = v.loadChunkWindow(ctx, entry, index, readWindowChunks(size))
	} else {
		data, err = v.loadChunk(ctx, entry, index)
	}
	if err != nil {
		return nil, err
	}
	return sliceChunkRange(data, start, size), nil
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

func (v *VFS) readChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	cacheKey := v.readCacheKey(entry)
	if cacheKey != "" {
		if hot, ok := v.hotChunk(cacheKey, index); ok {
			v.cache.addHit()
			return hot, nil
		}
		if cached, ok, err := v.cache.GetChunk(cacheKey, index); err != nil {
			return nil, err
		} else if ok {
			return cached, nil
		}
	}
	if data, ok, err := v.waitWindow(ctx, cacheKey, index); err != nil {
		return nil, err
	} else if ok {
		if data != nil {
			return data, nil
		}
		if cacheKey != "" {
			if cached, ok, err := v.cache.GetChunk(cacheKey, index); err != nil {
				return nil, err
			} else if ok {
				return cached, nil
			}
		}
	}
	if cacheKey != "" {
		return v.loadChunkWindow(ctx, entry, index, readPrefetchChunks)
	}
	return v.loadChunk(ctx, entry, index)
}

func readWindowChunks(requestSize int64) int {
	if requestSize > 0 && requestSize < readChunkSize {
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

type chunkLoad struct {
	done chan struct{}
	data []byte
	err  error
}

func (v *VFS) loadChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	key := readChunkKey(v.readLoadKey(entry), index)
	v.chunkLoadMu.Lock()
	if load := v.chunkLoads[key]; load != nil {
		v.chunkLoadMu.Unlock()
		select {
		case <-load.done:
			return load.data, load.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	load := &chunkLoad{done: make(chan struct{})}
	v.chunkLoads[key] = load
	v.chunkLoadMu.Unlock()

	load.data, load.err = v.fetchChunk(ctx, entry, index)
	close(load.done)

	v.chunkLoadMu.Lock()
	delete(v.chunkLoads, key)
	v.chunkLoadMu.Unlock()
	return load.data, load.err
}

func (v *VFS) loadChunkWindow(ctx context.Context, entry drive.Entry, startIndex int64, count int) ([]byte, error) {
	if count <= 1 {
		return v.loadChunk(ctx, entry, startIndex)
	}
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" {
		return v.loadChunk(ctx, entry, startIndex)
	}
	endIndex := startIndex + int64(count) - 1
	if entry.Size > 0 {
		lastIndex := (entry.Size - 1) / readChunkSize
		if endIndex > lastIndex {
			endIndex = lastIndex
		}
	}
	for endIndex > startIndex && v.readChunkAvailable(cacheKey, endIndex) {
		endIndex--
	}
	key := readWindowKey(cacheKey, startIndex, endIndex)
	v.windowLoadMu.Lock()
	if load := v.windowLoads[key]; load != nil {
		v.windowLoadMu.Unlock()
		select {
		case <-load.done:
			if load.err != nil {
				return nil, load.err
			}
			return load.data[startIndex], nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	v.windowLoads[key] = load
	v.windowLoadMu.Unlock()

	load.data, load.err = v.fetchChunkWindow(ctx, entry, startIndex, endIndex)
	close(load.done)

	v.windowLoadMu.Lock()
	delete(v.windowLoads, key)
	v.windowLoadMu.Unlock()
	if load.err != nil {
		return nil, load.err
	}
	return load.data[startIndex], nil
}

func (v *VFS) fetchChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	offset := index * readChunkSize
	if entry.Size > 0 && offset >= entry.Size {
		return nil, nil
	}
	size := int64(readChunkSize)
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	rc, err := v.driver.Read(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	data, err := readAllLimited(rc, size)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 0 {
		if cacheKey := v.readCacheKey(entry); cacheKey != "" {
			v.putHotChunk(cacheKey, index, data)
			v.cache.PutChunkAsync(cacheKey, entry.Size, index, data)
		}
	}
	return data, nil
}

func (v *VFS) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, startChunk, endChunk, requestSize int64) {
	v.prefetchChunk(ctx, entry, startChunk-readPrefetchRadius)
	v.prefetchWindow(ctx, entry, endChunk+1, readWindowChunks(requestSize))
}

type windowLoad struct {
	fid   string
	start int64
	end   int64
	done  chan struct{}
	data  map[int64][]byte
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

	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	v.windowLoadMu.Lock()
	v.windowLoads[key] = load
	v.windowLoadMu.Unlock()

	go func() {
		defer func() {
			close(load.done)
			v.windowLoadMu.Lock()
			delete(v.windowLoads, key)
			v.windowLoadMu.Unlock()
			<-v.prefetchSem
			v.prefetchMu.Lock()
			delete(v.prefetching, key)
			v.prefetchMu.Unlock()
		}()
		load.data, load.err = v.fetchChunkWindow(context.WithoutCancel(ctx), entry, startIndex, endIndex)
	}()
}

func (v *VFS) fetchChunkWindow(ctx context.Context, entry drive.Entry, startIndex, endIndex int64) (map[int64][]byte, error) {
	offset := startIndex * readChunkSize
	size := (endIndex - startIndex + 1) * readChunkSize
	if entry.Size > 0 && offset+size > entry.Size {
		size = entry.Size - offset
	}
	if size <= 0 {
		return nil, nil
	}
	rc, err := v.driver.Read(ctx, entry, offset, size)
	if err != nil {
		return nil, err
	}
	data, err := readAllLimited(rc, size)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
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
	return chunks, nil
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
	select {
	case <-load.done:
		if load.err != nil {
			return nil, true, load.err
		}
		return load.data[index], true, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

func (v *VFS) prefetchChunk(ctx context.Context, entry drive.Entry, index int64) {
	if index < 0 {
		return
	}
	if entry.Size > 0 && index*readChunkSize >= entry.Size {
		return
	}
	cacheKey := v.readCacheKey(entry)
	if cacheKey == "" {
		return
	}
	if v.readChunkAvailable(cacheKey, index) {
		return
	}
	key := readChunkKey(cacheKey, index)
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

	go func() {
		defer func() {
			<-v.prefetchSem
			v.prefetchMu.Lock()
			delete(v.prefetching, key)
			v.prefetchMu.Unlock()
		}()
		_, _ = v.loadChunk(context.WithoutCancel(ctx), entry, index)
	}()
}

func (v *VFS) readChunkAvailable(cacheKey string, index int64) bool {
	if _, ok := v.hotChunk(cacheKey, index); ok {
		return true
	}
	if ok, err := v.cache.HasChunk(cacheKey, index); err != nil || ok {
		return true
	}
	key := readChunkKey(cacheKey, index)
	v.chunkLoadMu.Lock()
	_, loading := v.chunkLoads[key]
	v.chunkLoadMu.Unlock()
	if loading {
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
