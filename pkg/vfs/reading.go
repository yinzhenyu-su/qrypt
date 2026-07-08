package vfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const readChunkSize = 512 * 1024
const readPrefetchRadius = 1
const readPrefetchLimit = 2
const readPrefetchChunks = 8

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
	pos := offset
	end, endKnown := readEnd(offset, size, entry.Size)
	for {
		if endKnown && pos >= end {
			break
		}
		chunkIndex := pos / readChunkSize
		chunk, err := v.readChunk(ctx, entry, chunkIndex)
		if err != nil {
			return nil, startChunk, endChunk, err
		}
		if len(chunk) == 0 {
			break
		}
		chunkStart := chunkIndex * readChunkSize
		start := pos - chunkStart
		if start >= int64(len(chunk)) {
			break
		}
		stop := int64(len(chunk))
		if endKnown && end-chunkStart < stop {
			stop = end - chunkStart
		}
		if stop > start {
			out.Write(chunk[start:stop])
			endChunk = chunkIndex
		}
		if len(chunk) < readChunkSize || (endKnown && chunkStart+stop >= end) {
			break
		}
		pos = chunkStart + stop
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

func (v *VFS) readChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	if cached, ok, err := v.cache.GetChunk(entry.ID, index); err != nil {
		return nil, err
	} else if ok {
		return cached, nil
	}
	if data, ok, err := v.waitWindow(ctx, entry.ID, index); err != nil {
		return nil, err
	} else if ok {
		if data != nil {
			return data, nil
		}
		if cached, ok, err := v.cache.GetChunk(entry.ID, index); err != nil {
			return nil, err
		} else if ok {
			return cached, nil
		}
	}
	return v.loadChunk(ctx, entry, index)
}

type chunkLoad struct {
	done chan struct{}
	data []byte
	err  error
}

func (v *VFS) loadChunk(ctx context.Context, entry drive.Entry, index int64) ([]byte, error) {
	key := readChunkKey(entry.ID, index)
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
	data, err := io.ReadAll(rc)
	closeErr := rc.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > 0 {
		if err := v.cache.PutChunk(entry.ID, index, data); err != nil {
			logging.L.Warnf("[CACHE] put chunk failed fid=%q index=%d size=%d err=%v", entry.ID, index, len(data), err)
		}
	}
	return data, nil
}

func (v *VFS) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, startChunk, endChunk int64) {
	v.prefetchChunk(ctx, entry, startChunk-readPrefetchRadius)
	v.prefetchWindow(ctx, entry, endChunk+1, readPrefetchChunks)
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
	endIndex := startIndex + int64(count) - 1
	for index := startIndex; index <= endIndex; index++ {
		if entry.Size > 0 && index*readChunkSize >= entry.Size {
			endIndex = index - 1
			break
		}
		if _, ok, err := v.cache.GetChunk(entry.ID, index); err != nil || ok {
			if index == startIndex {
				startIndex++
			}
			continue
		}
	}
	if endIndex < startIndex {
		return
	}
	key := readWindowKey(entry.ID, startIndex, endIndex)
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

	load := &windowLoad{fid: entry.ID, start: startIndex, end: endIndex, done: make(chan struct{})}
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
	data, err := io.ReadAll(rc)
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
		if err := v.cache.PutChunk(entry.ID, index, chunk); err != nil {
			logging.L.Warnf("[CACHE] put chunk failed fid=%q index=%d size=%d err=%v", entry.ID, index, len(chunk), err)
		}
		data = data[chunkSize:]
	}
	return chunks, nil
}

func (v *VFS) waitWindow(ctx context.Context, fid string, index int64) ([]byte, bool, error) {
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
	if _, ok, err := v.cache.GetChunk(entry.ID, index); err != nil || ok {
		return
	}
	key := readChunkKey(entry.ID, index)
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

func readChunkKey(fid string, index int64) string {
	return fid + "\x00" + strconv.FormatInt(index, 10)
}

func readWindowKey(fid string, start, end int64) string {
	return fid + "\x00" + strconv.FormatInt(start, 10) + "\x00" + strconv.FormatInt(end, 10)
}
