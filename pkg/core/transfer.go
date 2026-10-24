package core

// Shared URL transfer core.
//
// Offline fetch and share fetch transfer bytes through the same measured path:
// bounded Range requests (a provider may serve an unbounded request far more
// slowly), a fixed per-request plan, resume from the contiguous written prefix,
// and monotonic progress. The engine lives here so the two features cannot
// drift; feature-specific orchestration (what to transfer, where to, cleanup)
// stays in offline_fetch.go and share_fetch.go.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/task"
)

// offlineWriter is the destination writer of one transferred file.
type offlineWriter interface {
	Offset() int64
	WriteAt(p []byte, off int64) (int, error)
	// Close finalizes the transfer. For a namespace writer this flushes and
	// enqueues the upload, so a Close error means the destination was not
	// published.
	Close() error
	// Abort discards uncommitted bytes after a failed transfer so a partial
	// generation is never finalized or published.
	Abort() error
}

// offlineChunkPlan is the bounded-range plan for one provider download URL.
type offlineChunkPlan struct {
	// chunk is the byte span of one Range request.
	chunk int64
	// workers is how many Range requests run at the same time.
	workers int
}

func defaultOfflineChunkPlan() offlineChunkPlan {
	return offlineChunkPlan{chunk: offlineFetchChunk, workers: offlineFetchWorkers}
}

type offlineStreamResult struct {
	// written is the number of contiguously written bytes from the requested
	// start; a caller resuming after a failure continues at start+written.
	written int64
	// size is the total file size when the provider disclosed one.
	size int64
	// done reports that the whole file is present at the destination.
	done bool
}

// offlineURLFetch copies one provider download URL into a destination writer.
// Every request carries a bounded Range, because a provider may serve an
// unbounded request far more slowly. The first response also decides whether
// the provider honours ranges at all: a provider that ignores them gets a
// single sequential stream instead of parallel chunks.
type offlineURLFetch struct {
	client *http.Client
	target drive.OfflineDownloadTarget
	writer offlineWriter
	update task.UpdateFunc
	// base counts bytes already transferred for the whole task, so progress
	// stays monotonic across the files of a multi-file fetch.
	base int64
	plan offlineChunkPlan
	// progress is created by run for the duration of one URL attempt.
	progress *offlineProgress
}

func (f offlineURLFetch) run(ctx context.Context, start, size int64) (offlineStreamResult, error) {
	f.progress = &offlineProgress{update: f.update, base: f.base}
	if f.target.Size > 0 {
		size = f.target.Size
	}
	if size > 0 && start >= size {
		return offlineStreamResult{size: size, done: true}, nil
	}
	if f.plan.chunk <= 0 {
		f.plan.chunk = offlineFetchChunk
	}
	if f.plan.workers <= 0 {
		f.plan.workers = offlineFetchWorkers
	}
	if size <= 0 {
		return f.runUnknownSize(ctx, start)
	}
	if start+f.plan.chunk >= size {
		// One bounded response covers everything that remains.
		return f.runSingleRange(ctx, start, size-start, size)
	}
	return f.runChunks(ctx, start, size)
}

// runUnknownSize discovers whether the provider honours a bounded Range and how
// large the file is, using a one-byte request. A provider that ignores ranges
// answers 200 with the whole file, which is then streamed as-is.
func (f offlineURLFetch) runUnknownSize(ctx context.Context, start int64) (offlineStreamResult, error) {
	response, err := f.openRange(ctx, start, 1)
	if err != nil {
		return offlineStreamResult{}, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusRequestedRangeNotSatisfiable:
		if err := validateRangeNotSatisfiable(response, start, 0); err != nil {
			return offlineStreamResult{}, err
		}
		return offlineStreamResult{done: true}, nil
	case http.StatusOK:
		written, err := f.copyBody(ctx, response.Body, start, start, 0)
		if err != nil {
			return offlineStreamResult{written: written}, err
		}
		return offlineStreamResult{written: written, done: true}, nil
	case http.StatusPartialContent:
		if err := validatePartialResponse(response, start, 0); err != nil {
			return offlineStreamResult{}, err
		}
		size := contentRangeTotal(response)
		if _, err := io.CopyN(io.Discard, response.Body, 1); err != nil && err != io.EOF {
			return offlineStreamResult{}, err
		}
		if size <= 0 {
			result, err := f.runSequentialRanges(ctx, start, 0)
			result.size = 0
			return result, err
		}
		if start >= size {
			return offlineStreamResult{size: size, done: true}, nil
		}
		if start+f.plan.chunk >= size {
			return f.runSingleRange(ctx, start, size-start, size)
		}
		return f.runChunks(ctx, start, size)
	default:
		return offlineStreamResult{}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	}
}

// runSingleRange copies one bounded response that covers the file remainder.
func (f offlineURLFetch) runSingleRange(ctx context.Context, start, length, size int64) (offlineStreamResult, error) {
	response, err := f.openRange(ctx, start, length)
	if err != nil {
		return offlineStreamResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		// 416 is only success when the requested start really is at or past
		// the end of the file the provider reports. A provider that answers
		// 416 while bytes are still missing must not let the transfer claim
		// success (and let cleanup delete the cloud copy).
		if err := validateRangeNotSatisfiable(response, start, size); err != nil {
			return offlineStreamResult{size: size}, err
		}
		return offlineStreamResult{size: size, done: true}, nil
	}
	skip := int64(0)
	if response.StatusCode == http.StatusOK {
		// The provider ignored the Range header, so the body starts at zero.
		skip = start
	} else if response.StatusCode != http.StatusPartialContent {
		return offlineStreamResult{}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	} else if err := validatePartialResponse(response, start, size); err != nil {
		return offlineStreamResult{size: size}, err
	}
	written, err := f.copyBody(ctx, response.Body, start, skip, size)
	if err != nil {
		return offlineStreamResult{written: written, size: size}, err
	}
	return offlineStreamResult{written: written, size: size, done: size <= 0 || start+written >= size}, nil
}

// runChunks copies the remaining span with several bounded Range requests in
// flight. Chunks are handed out in order and a chunk that fails stops the
// attempt; the reported progress only counts the contiguous prefix so the next
// attempt restarts at the first gap.
func (f offlineURLFetch) runChunks(ctx context.Context, start, size int64) (offlineStreamResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu         sync.Mutex
		firstErr   error
		next       = start
		contiguous = start
		completed  = map[int64]int64{}
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	// wholeFile lets a provider that ignores ranges take over the transfer on
	// one connection while the other workers stop.
	wholeFile := false
	worker := func() {
		for {
			mu.Lock()
			if firstErr != nil || wholeFile || next >= size {
				mu.Unlock()
				return
			}
			offset := next
			next += f.plan.chunk
			mu.Unlock()
			length := min(f.plan.chunk, size-offset)
			response, err := f.openRange(ctx, offset, length)
			if err != nil {
				fail(err)
				return
			}
			if response.StatusCode == http.StatusOK {
				// Ranges are ignored: this response is the whole file from
				// zero. Stream it here; the other workers stop.
				mu.Lock()
				wholeFile = true
				mu.Unlock()
				copied, copyErr := f.copyBody(ctx, response.Body, start, start, size)
				response.Body.Close()
				if copyErr != nil {
					fail(copyErr)
					return
				}
				mu.Lock()
				completed = map[int64]int64{}
				contiguous = start + copied
				mu.Unlock()
				return
			}
			if response.StatusCode != http.StatusPartialContent {
				response.Body.Close()
				fail(fmt.Errorf("core: provider download URL returned status %d", response.StatusCode))
				return
			}
			if validateErr := validatePartialResponse(response, offset, size); validateErr != nil {
				response.Body.Close()
				fail(validateErr)
				return
			}
			copied, copyErr := f.copyChunk(ctx, response.Body, offset, length, f.progress.add)
			response.Body.Close()
			if copyErr != nil {
				fail(copyErr)
				return
			}
			if copied != length {
				fail(fmt.Errorf("core: provider returned %d of %d bytes for range %d", copied, length, offset))
				return
			}
			mu.Lock()
			completed[offset] = offset + copied
			for {
				end, ok := completed[contiguous]
				if !ok {
					break
				}
				delete(completed, contiguous)
				contiguous = end
			}
			mu.Unlock()
		}
	}
	workers := f.plan.workers
	if workers > 1 {
		var wait sync.WaitGroup
		for range workers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				worker()
			}()
		}
		wait.Wait()
	} else {
		worker()
	}
	mu.Lock()
	defer mu.Unlock()
	writtenFromStart := contiguous - start
	if firstErr != nil {
		return offlineStreamResult{written: writtenFromStart, size: size}, firstErr
	}
	return offlineStreamResult{written: writtenFromStart, size: size, done: contiguous >= size}, nil
}

// runSequentialRanges copies a provider file whose total size is unknown but
// that does honour ranges, one bounded chunk at a time, until a short chunk.
func (f offlineURLFetch) runSequentialRanges(ctx context.Context, start, known int64) (offlineStreamResult, error) {
	offset := start
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return offlineStreamResult{written: written}, err
		}
		response, err := f.openRange(ctx, offset, f.plan.chunk)
		if err != nil {
			return offlineStreamResult{written: written}, err
		}
		if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			response.Body.Close()
			if err := validateRangeNotSatisfiable(response, offset, known); err != nil {
				return offlineStreamResult{written: written}, err
			}
			return offlineStreamResult{written: written, done: true}, nil
		}
		if response.StatusCode == http.StatusOK {
			// Ranges are ignored after all: the body is the whole file.
			copied, copyErr := f.copyBody(ctx, response.Body, start, start, known)
			response.Body.Close()
			if copyErr != nil {
				return offlineStreamResult{written: copied}, copyErr
			}
			return offlineStreamResult{written: copied, done: true}, nil
		}
		if response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return offlineStreamResult{written: written}, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
		}
		if err := validatePartialResponse(response, offset, known); err != nil {
			response.Body.Close()
			return offlineStreamResult{written: written}, err
		}
		copied, copyErr := f.copyChunk(ctx, response.Body, offset, f.plan.chunk, f.progress.add)
		response.Body.Close()
		if copyErr != nil {
			return offlineStreamResult{written: written}, copyErr
		}
		offset += copied
		if copied < f.plan.chunk {
			return offlineStreamResult{written: written, done: true}, nil
		}
	}
}

// openRange issues one bounded Range request for the provider URL.
func (f offlineURLFetch) openRange(ctx context.Context, offset, length int64) (*http.Response, error) {
	if length <= 0 {
		length = 1
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.target.URL, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range f.target.Headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	response, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 && response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		response.Body.Close()
		return nil, fmt.Errorf("core: provider download URL returned status %d", response.StatusCode)
	}
	return response, nil
}

// copyBody streams a whole response body into the writer at start, skipping
// skip leading body bytes (a provider that ignored the Range sent the file from
// zero). A known size bounds the copy.
func (f offlineURLFetch) copyBody(ctx context.Context, body io.Reader, start, skip, size int64) (int64, error) {
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, body, skip); err != nil {
			if err == io.EOF {
				return 0, nil
			}
			return 0, err
		}
	}
	return f.copyChunk(ctx, body, start, remaining(size, start), f.progress.add)
}

// copyChunk copies up to length bytes of body to the writer at offset,
// reporting each buffer as it lands.
func (f offlineURLFetch) copyChunk(ctx context.Context, body io.Reader, offset, length int64, report func(int64)) (int64, error) {
	buffer := make([]byte, offlineTransferChunk)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if length > 0 && written >= length {
			return written, nil
		}
		want := buffer
		if length > 0 && length-written < int64(len(buffer)) {
			want = buffer[:length-written]
		}
		n, readErr := body.Read(want)
		if n > 0 {
			if _, writeErr := f.writer.WriteAt(want[:n], offset+written); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			report(int64(n))
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}

// offlineProgress publishes absolute transferred byte counts to the task. It is
// safe for the concurrent chunk workers.
type offlineProgress struct {
	update task.UpdateFunc
	base   int64

	mu    sync.Mutex
	total int64
}

// add reports delta more transferred bytes.
func (p *offlineProgress) add(delta int64) {
	if p == nil || p.update == nil {
		return
	}
	p.mu.Lock()
	p.total += delta
	done := p.base + p.total
	p.mu.Unlock()
	p.update(func(item *task.Task) {
		item.Progress.TransferBytesDone = done
	})
}

// contentRangeTotal reads the total size out of a Content-Range header,
// tolerating the `bytes start-end/total` and `bytes start-end/*` forms. A
// response without a usable total reports zero: a partial response's
// Content-Length only measures that chunk, so it is not a substitute.
func contentRangeTotal(response *http.Response) int64 {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if !ok || !rng.hasTotal {
		return 0
	}
	return rng.total
}

// offlineContentRange is a parsed Content-Range header. hasStart distinguishes
// "bytes 0-..." from an unsatisfied-range form ("bytes */N").
type offlineContentRange struct {
	start    int64
	end      int64
	total    int64
	hasStart bool
	hasEnd   bool
	hasTotal bool
}

// parseContentRangeHeader accepts `bytes start-end/total`, `bytes start-end/*`,
// and `bytes * /total` (without the space) forms.
func parseContentRangeHeader(value string) (offlineContentRange, bool) {
	value = strings.TrimSpace(value)
	spec, ok := strings.CutPrefix(value, "bytes ")
	if !ok {
		return offlineContentRange{}, false
	}
	span, total, ok := strings.Cut(strings.TrimSpace(spec), "/")
	if !ok {
		return offlineContentRange{}, false
	}
	var rng offlineContentRange
	span = strings.TrimSpace(span)
	if span != "" && span != "*" {
		startText, endText, ok := strings.Cut(span, "-")
		if !ok {
			return offlineContentRange{}, false
		}
		start, startErr := strconv.ParseInt(strings.TrimSpace(startText), 10, 64)
		end, endErr := strconv.ParseInt(strings.TrimSpace(endText), 10, 64)
		if startErr != nil || endErr != nil {
			return offlineContentRange{}, false
		}
		rng.start, rng.end, rng.hasStart, rng.hasEnd = start, end, true, true
	}
	total = strings.TrimSpace(total)
	if total != "" && total != "*" {
		parsed, err := strconv.ParseInt(total, 10, 64)
		if err != nil {
			return offlineContentRange{}, false
		}
		rng.total, rng.hasTotal = parsed, true
	}
	return rng, true
}

// validatePartialResponse verifies that a 206 response starts at the offset we
// asked for and, when both sides disclose a total, that the totals agree.
// Providers that omit Content-Range are tolerated because the sequential copy
// and the contiguous-prefix bookkeeping still guard correctness.
func validatePartialResponse(response *http.Response, offset, expectedTotal int64) error {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if !ok {
		return nil
	}
	if rng.hasStart && rng.start != offset {
		return fmt.Errorf("core: provider returned range starting at %d for request at %d", rng.start, offset)
	}
	if expectedTotal > 0 && rng.hasTotal && rng.total != expectedTotal {
		return fmt.Errorf("core: provider reports %d bytes but %d were expected", rng.total, expectedTotal)
	}
	return nil
}

// validateRangeNotSatisfiable decides whether a 416 means "the file ended
// exactly where we are" or "the provider rejected a range while bytes are still
// missing". Only the former may be reported as a complete file.
func validateRangeNotSatisfiable(response *http.Response, offset, expectedTotal int64) error {
	rng, ok := parseContentRangeHeader(response.Header.Get("Content-Range"))
	if expectedTotal > 0 {
		if ok && rng.hasTotal && rng.total != expectedTotal {
			return fmt.Errorf("core: provider reports %d bytes but %d were expected", rng.total, expectedTotal)
		}
		if offset >= expectedTotal {
			return nil
		}
		if ok && rng.hasTotal {
			return fmt.Errorf("core: provider rejected range at %d of %d bytes", offset, rng.total)
		}
		return fmt.Errorf("core: provider rejected range at %d without a usable size", offset)
	}
	if ok && rng.hasTotal && offset >= rng.total {
		return nil
	}
	return fmt.Errorf("core: provider rejected range at %d before the end of the file", offset)
}

// remaining is the number of bytes left from start when size is known, and 0
// (meaning "until EOF") when it is not.
func remaining(size, start int64) int64 {
	if size <= 0 || size <= start {
		return 0
	}
	return size - start
}

// transferPlanFor builds a chunk plan from caller tuning, falling back to the
// shared defaults. The bounds are validated at creation time.
func transferPlanFor(chunk int64, workers int) offlineChunkPlan {
	plan := defaultOfflineChunkPlan()
	if chunk > 0 {
		plan.chunk = chunk
	}
	if workers > 0 {
		plan.workers = workers
	}
	return plan
}

// streamDownloadTarget copies one provider download URL into a destination
// writer with the shared engine.
func streamDownloadTarget(ctx context.Context, target drive.DownloadTarget, start, size int64, writer offlineWriter, base int64, plan offlineChunkPlan, update task.UpdateFunc) (offlineStreamResult, error) {
	fetch := offlineURLFetch{
		client: offlineHTTPClient,
		target: target,
		writer: writer,
		update: update,
		base:   base,
		plan:   plan,
	}
	return fetch.run(ctx, start, size)
}
