package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/readbudget"
)

const (
	directReadPartSize = 8 << 20
	directReadSlots    = 16
)

// directReadScheduler is the direct-read budget. It uses the shared
// readbudget implementation but an independent capacity from any VFS mount.
var directReadScheduler = readbudget.NewScheduler(readbudget.SchedulerConfig{
	MaxConcurrency: directReadSlots,
	HighReserve:    0,
	PrefetchLimit:  1,
})

// ReadParallel reads [offset, offset+size) through the driver's read profile.
// size <= 0 means "whole file" when entry.Size is known. Drivers without
// CapabilityParallelRange, drivers that already parallelize internally, and
// files below the profile threshold fall back to one Read.
//
// Every read is bounded by the profile's fetch timeouts: the idle bound aborts
// a body that stops producing bytes, and the total bound caps the request as a
// whole. The returned body owns its request context and cancels it on Close.
func ReadParallel(ctx context.Context, d Driver, entry Entry, offset, size int64) (io.ReadCloser, error) {
	return ReadParallelWithProfile(ctx, d, entry, offset, size, ReadProfileOf(d))
}

// ReadParallelWithProfile is ReadParallel with an already-resolved effective
// profile. Callers use it when a mount overrides the driver defaults.
func ReadParallelWithProfile(ctx context.Context, d Driver, entry Entry, offset, size int64, profile ReadProfile) (io.ReadCloser, error) {
	timeouts := profile.FetchTimeouts()
	total := size
	if total <= 0 && entry.Size > 0 {
		total = entry.Size - offset
	}
	if total <= 0 || entry.Size <= 0 || profile.BulkStreams <= 1 || total < profile.ParallelThreshold {
		return singleStreamRead(ctx, d, entry, offset, total, timeouts)
	}
	if entry.Size > 0 && offset+total > entry.Size {
		total = entry.Size - offset
	}
	if total < profile.ParallelThreshold {
		return singleStreamRead(ctx, d, entry, offset, total, timeouts)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeouts.Total)
	probe, err := d.Read(WithStrictRange(probeCtx), entry, offset, 1)
	cancel()
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return singleStreamRead(ctx, d, entry, offset, total, timeouts)
		}
		return nil, classifyTimeoutError(ctx, err, timeouts.Total)
	}
	if err := probe.Close(); err != nil {
		return nil, err
	}
	return newParallelRangeReader(ctx, d, entry, offset, total, profile.BulkStreams, timeouts), nil
}

// singleStreamRead performs one bounded Read and returns a body that enforces
// the idle bound while the caller drains it.
//
// The total bound applies to one bounded request, not to a whole-file stream:
// a multi-gigabyte direct copy is not a single "fetch", and capping it at the
// fetch budget would abort a healthy transfer. The idle bound still protects
// the stream from a provider that stops sending.
func singleStreamRead(ctx context.Context, d Driver, entry Entry, offset, total int64, timeouts FetchTimeouts) (io.ReadCloser, error) {
	var readCtx context.Context
	var cancel context.CancelFunc
	if total > 0 && total <= directReadPartSize {
		readCtx, cancel = context.WithTimeout(ctx, timeouts.Total)
	} else {
		readCtx, cancel = context.WithCancel(ctx)
	}
	rc, err := d.Read(readCtx, entry, offset, total)
	if err != nil {
		cancel()
		return nil, classifyTimeoutError(ctx, err, timeouts.Total)
	}
	return NewFetchTimeoutBody(readCtx, cancel, rc, timeouts.Idle), nil
}

// classifyTimeoutError maps an expired fetch deadline to the timeout error
// semantic without hiding the caller's own cancellation.
func classifyTimeoutError(parent context.Context, err error, limit time.Duration) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if IsFetchTimeout(err) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &FetchTimeoutError{Kind: FetchTimeoutTotal, Limit: limit, Elapsed: limit}
	}
	return err
}

type partResult struct {
	data []byte
	err  error
}

type parallelRangeReader struct {
	pipe   *io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
}

func (r *parallelRangeReader) Read(p []byte) (int, error) { return r.pipe.Read(p) }

func (r *parallelRangeReader) Close() error {
	r.cancel()
	err := r.pipe.Close()
	<-r.done
	return err
}

func newParallelRangeReader(ctx context.Context, d Driver, entry Entry, offset, total int64, workers int, timeouts FetchTimeouts) *parallelRangeReader {
	ctx, cancel := context.WithCancel(ctx)
	pipeReader, pipeWriter := io.Pipe()
	done := make(chan struct{})
	reader := &parallelRangeReader{pipe: pipeReader, cancel: cancel, done: done}

	go func() {
		defer close(done)
		defer pipeWriter.Close()

		partCount := int((total + directReadPartSize - 1) / directReadPartSize)
		if partCount < 1 {
			partCount = 1
		}
		if workers > partCount {
			workers = partCount
		}
		results := make([]chan partResult, partCount)
		for i := range results {
			results[i] = make(chan partResult, 1)
		}
		jobs := make(chan int)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					partOffset := offset + int64(index)*directReadPartSize
					partSize := min(int64(directReadPartSize), total-int64(index)*directReadPartSize)
					data, err := readDirectPart(ctx, d, entry, partOffset, partSize, timeouts)
					select {
					case results[index] <- partResult{data: data, err: err}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		go func() {
			defer close(jobs)
			for index := 0; index < partCount; index++ {
				select {
				case jobs <- index:
				case <-ctx.Done():
					return
				}
			}
		}()
		for index := 0; index < partCount; index++ {
			var result partResult
			select {
			case result = <-results[index]:
			case <-ctx.Done():
				return
			}
			if result.err != nil {
				_ = pipeWriter.CloseWithError(result.err)
				return
			}
			if _, err := pipeWriter.Write(result.data); err != nil {
				return
			}
		}
		wg.Wait()
	}()
	return reader
}

func readDirectPart(ctx context.Context, d Driver, entry Entry, offset, size int64, timeouts FetchTimeouts) ([]byte, error) {
	release, err := directReadScheduler.AcquireSlot(ctx, readbudget.PriorityNormal)
	if err != nil {
		return nil, err
	}
	defer release()

	partCtx, cancel := context.WithTimeout(ctx, timeouts.Total)
	defer cancel()
	rc, err := d.Read(WithStrictRange(partCtx), entry, offset, size)
	if err != nil {
		return nil, classifyTimeoutError(ctx, err, timeouts.Total)
	}
	data, err := ReadBodyLimited(partCtx, cancel, rc, size, timeouts.Idle)
	if err != nil {
		_ = rc.Close()
		return nil, classifyTimeoutError(ctx, err, timeouts.Total)
	}
	if err := rc.Close(); err != nil {
		return nil, err
	}
	if int64(len(data)) > size {
		return nil, fmt.Errorf("drive: read returned more than requested: limit=%d read=%d", size, len(data))
	}
	return data, nil
}
