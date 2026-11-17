package drive

import (
	"context"
	"math"
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

// FetchPart executes one part of a window with the same strict-range, timeout,
// retry and truncated-body boundary a window fetch uses for its parts, handing
// each complete ReadChunkSize block to emit in offset order. Admission happens
// per Range inside OpenRange, so a read-layer worker pool bounds concurrency by
// its own worker count.
func (s *ReadSource) FetchPart(ctx context.Context, e Entry, offset, size int64, plan RangePlan, emit func(offset int64, block []byte)) error {
	if emit == nil || e.ID == "" || offset < 0 || size <= 0 || offset > math.MaxInt64-size {
		return ErrInvalidInput
	}
	plan.Offset, plan.Size = offset, size
	delivery := &rangeDelivery{next: offset, emit: emit}
	_, err := s.fetchPart(ctx, e, readRange{offset, size}, plan, delivery)
	return err
}
