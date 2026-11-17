package drive

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// FetchStream is Fetch with progressive delivery: every complete
// ReadChunkSize block is handed to emit, in offset order, as soon as the
// window has fetched it. The trailing block is delivered only after the
// window's size and body have validated, so a failed tail cannot revoke blocks
// the consumer already accepted. Windows that cannot stream (unknown object
// size, unaligned offset) degrade to a buffered fetch published at the end.
//
// The returned bytes are nil; the caller owns each emitted block through emit.
func (s *ReadSource) FetchStream(ctx context.Context, e Entry, plan RangePlan, emit func(offset int64, block []byte)) error {
	if emit == nil {
		return ErrInvalidInput
	}
	delivery := &rangeDelivery{next: plan.Offset, emit: emit}
	_, err := s.fetch(ctx, e, plan, delivery)
	return err
}

// fetchProgressively serves a window whose blocks must be visible before the
// window finishes. A one-stream window is delivered block by block by the
// range layer; a parallel window assembles its parts in place and publishes
// each block once every part covering it has completed.
func (s *ReadSource) fetchProgressively(ctx context.Context, e Entry, plan RangePlan, layout rangeLayout, count int64, strict bool, delivery *rangeDelivery) ([]byte, error) {
	if plan.Offset%ReadChunkSize != 0 {
		// Block boundaries are chunk aligned; an unaligned window keeps the
		// buffered contract and publishes completed blocks once validated.
		data, err := s.fetch(ctx, e, plan, nil)
		if err != nil {
			return nil, err
		}
		for len(data) > 0 {
			n := min(int64(len(data)), ReadChunkSize)
			delivery.publish(data[:n:n])
			data = data[n:]
		}
		return nil, nil
	}
	if layout.workers <= 1 {
		return s.fetchPart(ctx, e, readRange{plan.Offset, plan.Size}, plan, delivery)
	}
	return s.streamWindow(ctx, e, plan, layout, count, strict, delivery)
}

// streamWindow runs the parallel part workers and publishes completed blocks
// as they become contiguous. A strict-range rejection falls back to finishing
// the unpublished suffix on one stream, so published blocks are not repeated.
func (s *ReadSource) streamWindow(ctx context.Context, e Entry, plan RangePlan, layout rangeLayout, count int64, strict bool, delivery *rangeDelivery) ([]byte, error) {
	data := make([]byte, int(plan.Size))
	stream := newChunkStream(data, delivery)
	var next atomic.Int64
	var short atomic.Int64
	short.Store(plan.Size)
	group, gctx := errgroup.WithContext(ctx)
	for range layout.workers {
		group.Go(func() error {
			for {
				index := next.Add(1) - 1
				if index >= count {
					return nil
				}
				span := layout.at(index)
				start := span.Offset - plan.Offset
				partCtx := gctx
				if strict {
					partCtx = WithStrictRange(partCtx)
				}
				piece, err := s.fetchPart(partCtx, e, span, plan, nil)
				if err != nil {
					return err
				}
				if int64(len(piece)) > span.Size {
					return ErrInvalidInput
				}
				stream.put(start, piece)
				if int64(len(piece)) < span.Size {
					short.Store(start + int64(len(piece)))
					return nil
				}
			}
		})
	}
	err := group.Wait()
	if err != nil {
		if strict && errors.Is(err, ErrUnsupported) {
			s.scheduler.MarkNonParallel(e.ID)
			plan.Streams = 1
			plan.Split.PartBytes = 0
			remaining := plan.Offset + plan.Size - delivery.next
			if remaining > 0 {
				if _, partErr := s.fetchPart(ctx, e, readRange{delivery.next, remaining}, plan, delivery); partErr != nil {
					return nil, partErr
				}
			}
			return data[:short.Load()], nil
		}
		return nil, err
	}
	valid := short.Load()
	stream.finish(valid)
	return data[:valid], nil
}

// chunkStream assembles a parallel window in place and publishes each complete
// ReadChunkSize block to the window's delivery, in offset order. The last block
// is held back until the window validates.
type chunkStream struct {
	data     []byte
	chunk    int64
	delivery *rangeDelivery
	mu       sync.Mutex
	filled   []int32
	want     []int32
	next     int64
}

func newChunkStream(data []byte, delivery *rangeDelivery) *chunkStream {
	count := (int64(len(data)) + ReadChunkSize - 1) / ReadChunkSize
	stream := &chunkStream{
		data:     data,
		chunk:    ReadChunkSize,
		delivery: delivery,
		filled:   make([]int32, count),
		want:     make([]int32, count),
	}
	for index := range stream.want {
		stream.want[index] = int32(min(int64(len(data))-int64(index)*ReadChunkSize, ReadChunkSize))
	}
	return stream
}

// put copies one completed part into the window buffer and publishes the
// blocks it completed, except the trailing block.
func (c *chunkStream) put(start int64, piece []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy(c.data[start:], piece)
	for pos, end := start, start+int64(len(piece)); pos < end; {
		index := pos / c.chunk
		chunkEnd := min((index+1)*c.chunk, int64(len(c.data)))
		next := min(end, chunkEnd)
		c.filled[index] += int32(next - pos)
		pos = next
	}
	for c.next < int64(len(c.filled))-1 && c.filled[c.next] >= c.want[c.next] {
		c.publishLocked(c.next)
	}
}

// finish publishes the withheld trailing block once the validated window
// length is known.
func (c *chunkStream) finish(valid int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	valid = max(min(valid, int64(len(c.data))), 0)
	for c.next < int64(len(c.filled)) {
		start := c.next * c.chunk
		if start >= valid {
			return
		}
		end := min(start+c.chunk, valid)
		if c.filled[c.next] < int32(end-start) {
			return
		}
		c.delivery.publish(c.data[start:end:end])
		c.next++
	}
}

func (c *chunkStream) publishLocked(index int64) {
	start := index * c.chunk
	end := start + int64(c.want[index])
	c.delivery.publish(c.data[start:end:end])
	c.next++
}
