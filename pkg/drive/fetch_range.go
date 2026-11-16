package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type rangeOpener func(context.Context, Entry, int64, int64) (io.ReadCloser, error)

const (
	// fetchTimeoutAttempts is the total number of tries for one window range
	// after an idle stall: the original Range GET plus one idempotent retry.
	// Buffered reads replay the whole range; progressive reads resume at the
	// first unpublished block and never replace an already delivered prefix.
	fetchTimeoutAttempts = 2
	// fetchStatusAttempts is the total number of tries for one window range
	// when the provider answers with a retryable status (5xx, 408, 429). A
	// transient provider hiccup then costs one or two extra requests instead of
	// a failed FUSE read, while a persistent failure still fails fast: three
	// attempts bound the extra latency at two backoffs plus two requests.
	fetchStatusAttempts = 3
	// fetchStatusBackoffBase is the wait before the first status replay; it
	// doubles per replay and is capped by fetchStatusBackoffMax. The wait is
	// charged to the range's total budget, which also bounds the sum.
	fetchStatusBackoffBase = 200 * time.Millisecond
	fetchStatusBackoffMax  = 2 * time.Second
)

// rangeDelivery transfers ownership of complete blocks once, in offset order.
// A retry resumes at next; bytes already handed to the consumer are immutable.
type rangeDelivery struct {
	next int64
	emit func(int64, []byte)
}

func (d *rangeDelivery) publish(data []byte) {
	d.emit(d.next, data)
	d.next += int64(len(data))
}

func fetchRange(ctx context.Context, entry Entry, offset, size int64, timeouts FetchTimeouts, open rangeOpener, delivery *rangeDelivery) ([]byte, map[string]any, error) {
	if entry.Size > 0 {
		size = min(size, entry.Size-offset)
	}
	if size <= 0 {
		return nil, nil, nil
	}
	// One deadline covers every attempt: an idle-stall replay or a status
	// backoff may only spend what is left of the range's total budget.
	deadline := time.Now().Add(timeouts.Total)
	var extra map[string]any
	var lastErr error
	attempts := 0
	timeoutCount := 0
	timeoutKind := FetchTimeoutIdle
	statusRetries := 0
	statusReason := ""
	bodyRetries := 0
	var backoffTotal time.Duration
	recordRetries := func() {
		if extra == nil {
			return
		}
		if attempts > 1 {
			extra["fetch_attempts"] = attempts
			extra["fetch_retried"] = true
		}
		if timeoutCount > 0 {
			extra["fetch_timeouts"] = timeoutCount
			extra["fetch_timeout_kind"] = string(timeoutKind)
		}
		if statusRetries > 0 {
			extra["fetch_status_retries"] = statusRetries
			extra["fetch_status_reason"] = statusReason
			extra["fetch_backoff_ms"] = durationMillis(backoffTotal)
		}
		if bodyRetries > 0 {
			extra["fetch_body_retries"] = bodyRetries
		}
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attempts++
		attemptOffset, attemptSize := offset, size
		if delivery != nil {
			attemptOffset = delivery.next
			attemptSize = offset + size - attemptOffset
		}
		data, attemptExtra, err := readWindowRangeAttempt(ctx, entry, attemptOffset, attemptSize, timeouts.Idle, remaining, open, delivery)
		if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
			trace.record(attemptExtra)
		}
		extra = attemptExtra
		if err == nil {
			recordRetries()
			return data, extra, nil
		}
		lastErr = err
		var timeout *FetchTimeoutError
		if errors.As(err, &timeout) {
			timeoutCount++
			timeoutKind = timeout.Kind
			// Only an idle stall is worth replaying: a total deadline has no
			// budget left, and other failures are not the timeout case.
			if timeout.Kind == FetchTimeoutIdle && timeoutCount < fetchTimeoutAttempts && ctx.Err() == nil {
				continue
			}
			recordRetries()
			return nil, extra, err
		}
		if isShortBodyError(err) {
			// A body that ends before its declared range is a provider hiccup,
			// not a permanent failure: replay the Range GET once. This keeps
			// truncated-body resilience in the shared read layer instead of
			// asking every driver to re-request partial responses itself.
			if ctx.Err() != nil || bodyRetries+1 >= fetchTimeoutAttempts {
				recordRetries()
				return nil, extra, err
			}
			bodyRetries++
			continue
		}
		reason := readRetryReason(err)
		if reason == "" || statusRetries+1 >= fetchStatusAttempts || ctx.Err() != nil {
			recordRetries()
			return nil, extra, err
		}
		statusRetries++
		statusReason = reason
		backoff := fetchStatusBackoff(statusRetries)
		if backoff >= time.Until(deadline) {
			// The wait would outlive the range's budget: reporting the provider
			// failure is more useful than the timeout we would cause.
			recordRetries()
			return nil, extra, err
		}
		if sleepErr := sleepContext(ctx, backoff); sleepErr != nil {
			recordRetries()
			return nil, extra, sleepErr
		}
		backoffTotal += backoff
	}
	recordRetries()
	if extra == nil {
		extra = map[string]any{}
	}
	if lastErr != nil {
		return nil, extra, lastErr
	}
	return nil, extra, &FetchTimeoutError{Kind: FetchTimeoutTotal, Limit: timeouts.Total}
}

// isShortBodyError reports whether err means the body ended before the
// declared range: a clean short read, or a body closed underneath the reader.
// The second form is how an aborted HTTP/2 stream or a decryption wrapper
// reports a truncated fetch, and it is just as replayable.
func isShortBodyError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "response body closed") || strings.Contains(message, "closed response body")
}

// readRetryReason reports why a failed Range GET is worth replaying, or "" when
// it is not. Range GETs are idempotent, so a provider hiccup costs one extra
// request instead of a failed read; permanent failures (auth, permission, not
// found, invalid input, unsupported range) keep their fast path.
func readRetryReason(err error) string {
	if err == nil || IsNonRetryable(err) {
		return ""
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch {
		case status.Status == http.StatusTooManyRequests:
			return "rate_limited"
		case status.Status == http.StatusRequestTimeout:
			return "request_timeout"
		case status.Status >= 500:
			return "status_5xx"
		}
	}
	if errors.Is(err, ErrRateLimit) {
		return "rate_limited"
	}
	return ""
}

// fetchStatusBackoff returns the wait before the n-th status replay (1-based):
// base, base*2, base*4, capped at fetchStatusBackoffMax.
func fetchStatusBackoff(retries int) time.Duration {
	delay := fetchStatusBackoffBase
	for i := 1; i < retries && delay < fetchStatusBackoffMax; i++ {
		delay *= 2
	}
	return min(delay, fetchStatusBackoffMax)
}

// sleepContext waits for d, or returns early when ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readWindowRangeAttempt performs one Range GET bounded by the idle and total
// limits. total is the remaining share of the fetch deadline, so opening the
// request and draining the body share one budget.
func readWindowRangeAttempt(ctx context.Context, entry Entry, offset, size int64, idle, total time.Duration, open rangeOpener, delivery *rangeDelivery) ([]byte, map[string]any, error) {
	// The abandon cause tells the driver *why* the request context ends. A
	// provider stall must reach the driver as a stalled connection so it can
	// rotate away from a throttled one; a caller cancel must not.
	deadlineCtx, cancelDeadline := context.WithTimeoutCause(ctx, total, ReadTotalDeadline)
	defer cancelDeadline()
	attemptCtx, cancelAttempt := context.WithCancelCause(deadlineCtx)
	defer cancelAttempt(nil)
	// A driver-side bandwidth limiter may hold the body while the provider is
	// fine; the idle bound must not read that as a stall.
	attemptCtx = WithLocalWaitTracking(attemptCtx)
	rc, err := open(attemptCtx, entry, offset, size)
	extra := map[string]any{}
	if err != nil {
		return nil, extra, classifyTimeoutError(ctx, deadlineCtx, err, total)
	}
	defer rc.Close()
	bodyStarted := time.Now()
	var data []byte
	if delivery == nil {
		data, err = ReadBodyLimited(attemptCtx, cancelAttempt, rc, size, idle)
	} else {
		data, err = readBodyChunks(attemptCtx, cancelAttempt, rc, size, idle, delivery)
	}
	bodyFinished := time.Now()
	extra["driver_body_ms"] = durationMillis(bodyFinished.Sub(bodyStarted))
	extra["body_us"] = bodyFinished.Sub(bodyStarted).Microseconds()
	extra["local_wait_us"] = localWaitWatchFrom(attemptCtx).duration().Microseconds()
	closeStarted := time.Now()
	closeErr := rc.Close()
	closeFinished := time.Now()
	extra["driver_close_ms"] = durationMillis(closeFinished.Sub(closeStarted))
	if err != nil {
		return nil, extra, classifyTimeoutError(ctx, deadlineCtx, err, total)
	}
	validateStarted := time.Now()
	defer func() { extra["validate_us"] = time.Since(validateStarted).Microseconds() }()
	if delivery == nil && entry.Size > 0 && offset+size <= entry.Size && int64(len(data)) != size {
		// A body that ends cleanly before its declared range looks like a
		// short read, not an error. Report it as a truncated body so the
		// window layer replays the Range GET instead of assembling a window
		// from partial data.
		return nil, extra, fmt.Errorf("read: returned %d of %d bytes: %w", len(data), size, io.ErrUnexpectedEOF)
	}
	if closeErr != nil {
		return nil, extra, closeErr
	}
	if delivery != nil {
		if err := ctx.Err(); err != nil {
			return nil, extra, err
		}
		delivery.publish(data) // Final block waits for EOF and successful Close.
		return nil, extra, nil
	}
	return data, extra, nil
}

// readBodyChunks shares the timeout body with buffered fetches. Only complete
// non-final blocks are handed off early. The final block remains private until
// EOF, size and Close validation succeed. A failed tail cannot revoke a prefix
// already consumed, just as with an ordinary streaming reader.
func readBodyChunks(ctx context.Context, cancel context.CancelCauseFunc, body io.ReadCloser, size int64, idle time.Duration, delivery *rangeDelivery) ([]byte, error) {
	reader := newTimeoutBody(ctx, func() { cancel(nil) }, func() { cancel(ReadIdleStall) }, body, idle)
	defer reader.finish()
	remaining := size
	for remaining > 0 {
		chunk := make([]byte, int(min(int64(ReadChunkSize), remaining)))
		if _, err := io.ReadFull(reader, chunk); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining -= int64(len(chunk))
		if remaining == 0 {
			var extra [1]byte
			n, err := reader.Read(extra[:])
			if n != 0 {
				return nil, fmt.Errorf("drive: driver returned more data than requested: limit=%d", size)
			}
			if err != io.EOF {
				if err == nil {
					err = io.ErrNoProgress
				}
				return nil, err
			}
			return chunk, nil
		}
		delivery.publish(chunk)
	}
	return nil, io.ErrUnexpectedEOF
}
