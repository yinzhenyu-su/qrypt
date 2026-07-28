package vfs

import (
	"context"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (v *VFS) prefetchAdjacentChunks(ctx context.Context, entry drive.Entry, endChunk int64, windowChunks int) {
	if windowChunks <= 0 {
		return
	}
	v.prefetchWindow(ctx, entry, endChunk+1, windowChunks)
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
	runtime := newVFSReadRuntime(v)
	if !runtime.ReservePrefetch(key) {
		return
	}
	prefetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	prefetchCtx = WithReadPriority(prefetchCtx, PriorityLow)

	load := &windowLoad{fid: cacheKey, start: startIndex, end: endIndex, done: make(chan struct{})}
	if _, exists := runtime.BeginWindowLoad(key, load); exists {
		runtime.ReleasePrefetch(key)
		cancel()
		return
	}

	go func() {
		activeID := v.beginDebugActive(DebugActiveOp{
			Kind:        "vfs_prefetch",
			Phase:       "acquire_slot",
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
		releaseSlot, err := v.acquireReadSlot(prefetchCtx)
		if err != nil {
			load.err = err
			v.finishDebugActive(activeID)
			close(load.done)
			runtime.EndWindowLoad(key)
			runtime.ReleasePrefetch(key)
			cancel()
			return
		}
		defer func() {
			releaseSlot()
			v.finishDebugActive(activeID)
			close(load.done)
			runtime.EndWindowLoad(key)
			runtime.ReleasePrefetch(key)
			cancel()
		}()
		v.updateDebugActive(activeID, func(op *DebugActiveOp) { op.Phase = "fetch_window" })
		load.data, load.extra, load.err = v.fetchChunkWindow(prefetchCtx, entry, startIndex, endIndex)
	}()
}
