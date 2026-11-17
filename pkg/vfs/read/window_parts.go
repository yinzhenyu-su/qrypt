package read

import (
	"context"
	"errors"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// windowBlocks reassembles part deliveries into ReadChunkSize blocks and hands
// each block to emit in offset order. Parts complete out of order and may end
// mid-block, so the joiner buffers pieces by offset until the gap before them
// fills.
type windowBlocks struct {
	mu        sync.Mutex
	next      int64
	cur       []byte
	pending   map[int64][]byte
	emit      func(offset int64, block []byte)
	published int64
}

func newWindowBlocks(offset int64, emit func(offset int64, block []byte)) *windowBlocks {
	return &windowBlocks{next: offset, pending: map[int64][]byte{}, emit: emit, published: offset}
}

// put accepts one delivered piece. Pieces may arrive in any order; contiguous
// data is flushed as complete chunk blocks.
func (b *windowBlocks) put(offset int64, piece []byte) {
	if len(piece) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if offset != b.next {
		b.pending[offset] = piece
		return
	}
	b.cur = append(b.cur, piece...)
	b.drain()
}

func (b *windowBlocks) drain() {
	for {
		for len(b.cur) >= ChunkSize {
			block := b.cur[:ChunkSize:ChunkSize]
			b.emit(b.published, block)
			b.published += ChunkSize
			b.cur = b.cur[ChunkSize:]
		}
		b.next = b.published + int64(len(b.cur))
		piece, ok := b.pending[b.next]
		if !ok {
			return
		}
		delete(b.pending, b.next)
		b.cur = append(b.cur, piece...)
	}
}

// finish publishes the window's trailing partial block once every part has
// completed. Nothing is published after a failed part beyond what a full block
// already covered.
func (b *windowBlocks) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.cur) > 0 {
		b.emit(b.published, b.cur[:len(b.cur):len(b.cur)])
		b.published += int64(len(b.cur))
		b.cur = nil
	}
	b.next = b.published
}

// publishedEnd is the end offset of the last emitted block. It is block aligned
// unless finish published a partial tail.
func (b *windowBlocks) publishedEnd() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.published
}

// windowPart is one part's span inside the window.
type windowPart struct {
	offset, size int64
}

// windowParts describes how the read layer splits a streaming window into parts.
type windowParts struct {
	offset, size, part int64
	workers            int
	count              int64
}

func newWindowParts(offset, size int64, workers int, part int64) windowParts {
	workers = max(1, workers)
	if part <= 0 {
		part = (size + int64(workers) - 1) / int64(workers)
	}
	if part <= 0 {
		part = size
	}
	return windowParts{offset: offset, size: size, part: part, workers: workers, count: (size + part - 1) / part}
}

func (p windowParts) at(index int64) windowPart {
	start := index * p.part
	return windowPart{offset: p.offset + start, size: min(p.part, p.size-start)}
}

// streamWindowParts runs the part workers of one streaming window from the read
// layer: it owns the worker count and part size, hands each part to the drive
// source as a single admitted Range, and reassembles the accepted blocks so
// waiters see them as soon as they are complete.
//
// A strict-range rejection falls back to finishing the unpublished suffix on a
// single stream, so blocks already published are never fetched twice.
func (r *Reader) streamWindowParts(ctx context.Context, entry drive.Entry, w windowFetch, startIndex int64, load *windowLoad, backend readWindowBackend) (int64, error) {
	if w.windowBytes <= 0 {
		return 0, nil
	}
	parts := newWindowParts(startIndex*ChunkSize, w.windowBytes, w.streams, w.partBytes)
	plan := w.rangePlan(startIndex)
	delivered := int64(0)
	publish := func(offset int64, block []byte) {
		index := offset / ChunkSize
		backend.StoreChunk(ctx, backend.CacheKey(entry), entry, index, block)
		load.data[index] = block
		load.publish(index, block)
		delivered += int64(len(block))
	}
	blocks := newWindowBlocks(parts.offset, publish)

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	jobs := make(chan int64)
	for range parts.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if workerCtx.Err() != nil {
					return
				}
				span := parts.at(index)
				if err := r.source.FetchPart(workerCtx, entry, span.offset, span.size, plan, blocks.put); err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
			}
		}()
	}
	for index := int64(0); index < parts.count; index++ {
		select {
		case jobs <- index:
		case <-workerCtx.Done():
			index = parts.count
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr == nil {
		blocks.finish()
		return delivered, nil
	}
	if !errors.Is(firstErr, drive.ErrUnsupported) {
		return delivered, firstErr
	}
	// The driver rejects parallel ranges: finish what is not published yet on a
	// single stream, keeping delivered blocks valid.
	r.source.Scheduler().MarkNonParallel(entry.ID)
	end := parts.offset + parts.size
	from := blocks.publishedEnd()
	if from >= end {
		return delivered, nil
	}
	fallback := plan
	fallback.Offset, fallback.Size = from, end-from
	fallback.Workers, fallback.Part = 0, 0
	fallback.Streams = 1
	fallback.Split = drive.SplitPolicy{}
	cont := newWindowBlocks(from, publish)
	if err := r.source.FetchStream(ctx, entry, fallback, cont.put); err != nil {
		return delivered, err
	}
	cont.finish()
	return delivered, nil
}
