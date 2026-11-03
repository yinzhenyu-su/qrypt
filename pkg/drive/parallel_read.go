package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	directReadPartSize = 8 << 20
	directReadSlots    = 16
)

var directReadSlotPool = make(chan struct{}, directReadSlots)

// ReadParallel reads [offset, offset+size) through the driver's read profile.
// size <= 0 means "whole file" when entry.Size is known. Drivers without
// CapabilityParallelRange, drivers that already parallelize internally, and
// files below the profile threshold fall back to one Read.
func ReadParallel(ctx context.Context, d Driver, entry Entry, offset, size int64) (io.ReadCloser, error) {
	profile := ReadProfileOf(d)
	total := size
	if total <= 0 && entry.Size > 0 {
		total = entry.Size - offset
	}
	if total <= 0 || entry.Size <= 0 || profile.BulkStreams <= 1 || total < profile.ParallelThreshold {
		return d.Read(ctx, entry, offset, total)
	}
	if entry.Size > 0 && offset+total > entry.Size {
		total = entry.Size - offset
	}
	if total < profile.ParallelThreshold {
		return d.Read(ctx, entry, offset, total)
	}
	probe, err := d.Read(WithStrictRange(ctx), entry, offset, 1)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return d.Read(ctx, entry, offset, total)
		}
		return nil, err
	}
	if err := probe.Close(); err != nil {
		return nil, err
	}
	return newParallelRangeReader(ctx, d, entry, offset, total, profile.BulkStreams), nil
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

func newParallelRangeReader(ctx context.Context, d Driver, entry Entry, offset, total int64, workers int) *parallelRangeReader {
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
					data, err := readDirectPart(ctx, d, entry, partOffset, partSize)
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

func readDirectPart(ctx context.Context, d Driver, entry Entry, offset, size int64) ([]byte, error) {
	select {
	case directReadSlotPool <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-directReadSlotPool }()

	rc, err := d.Read(WithStrictRange(ctx), entry, offset, size)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > size {
		return nil, fmt.Errorf("drive: read returned more than requested: limit=%d read=%d", size, len(data))
	}
	return data, nil
}
