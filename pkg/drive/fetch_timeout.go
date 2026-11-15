package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// FetchTimeoutKind identifies which bound aborted a read fetch.
type FetchTimeoutKind string

const (
	// FetchTimeoutIdle means the body produced no bytes for the idle bound.
	FetchTimeoutIdle FetchTimeoutKind = "idle"
	// FetchTimeoutTotal means the whole fetch reached its total bound.
	FetchTimeoutTotal FetchTimeoutKind = "total"
)

// FetchTimeoutError reports that a read fetch was aborted by a timeout. It
// unwraps to context.DeadlineExceeded so ErrorCategory classifies it as
// ErrorCategoryTimeout and RetryableCategory treats it as transient.
type FetchTimeoutError struct {
	Kind    FetchTimeoutKind
	Limit   time.Duration
	Elapsed time.Duration
}

func (e *FetchTimeoutError) Error() string {
	if e == nil {
		return "drive: read fetch timeout"
	}
	bound := "idle"
	if e.Kind == FetchTimeoutTotal {
		bound = "total"
	}
	return fmt.Sprintf("drive: read fetch %s timeout after %s (limit %s)",
		bound, e.Elapsed.Round(time.Millisecond), e.Limit.Round(time.Millisecond))
}

// Unwrap makes the timeout satisfy errors.Is(err, context.DeadlineExceeded).
func (e *FetchTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// IsFetchTimeout reports whether err was produced by a read fetch timeout.
func IsFetchTimeout(err error) bool {
	var timeout *FetchTimeoutError
	return errors.As(err, &timeout)
}

// FetchTimeouts is a resolved read-fetch timeout pair.
type FetchTimeouts struct {
	// Idle bounds how long one fetch may produce no bytes.
	Idle time.Duration
	// Total bounds one complete fetch, including the request round trip.
	Total time.Duration
}

// FetchTimeouts resolves the profile's timeouts, filling defaults. The idle
// bound never exceeds the total bound: an idle bound that cannot fire before
// the total one is misleading.
func (p ReadProfile) FetchTimeouts() FetchTimeouts {
	out := FetchTimeouts{Idle: p.FetchIdleTimeout, Total: p.FetchTotalTimeout}
	if out.Idle <= 0 {
		out.Idle = DefaultFetchIdleTimeout
	}
	if out.Total <= 0 {
		out.Total = DefaultFetchTotalTimeout
	}
	if out.Idle > out.Total {
		out.Idle = out.Total
	}
	return out
}

// ReadBodyLimited reads at most limit bytes from body.
//
// The idle bound is enforced here because a context cannot express "no byte
// arrived for N seconds": a provider can keep a request open while sending
// nothing. The total bound is the caller's context deadline (derived with
// context.WithTimeout) and also covers the request round trip. cancel must
// cancel the context that created body; it unblocks the driver when the idle
// bound fires or the caller gives up.
func ReadBodyLimited(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, limit int64, idle time.Duration) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("drive: read limit must be non-negative")
	}
	if body == nil {
		return nil, fmt.Errorf("drive: read body is nil")
	}
	if idle <= 0 {
		idle = DefaultFetchIdleTimeout
	}
	reader := newTimeoutBody(ctx, cancel, body, idle)
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		reader.Close()
		return nil, err
	}
	// The read finished naturally: stop the watchdog but leave the body to
	// the caller, which owns closing the completed response.
	reader.finish()
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("drive: driver returned more data than requested: limit=%d read=%d", limit, len(data))
	}
	return data, nil
}

// NewFetchTimeoutBody wraps body so a blocked Read is abandoned after idle
// with no new bytes. cancel must cancel the context that created body. The
// total bound is expected to come from ctx's deadline. Closing the returned
// body cancels the request.
func NewFetchTimeoutBody(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, idle time.Duration) io.ReadCloser {
	if idle <= 0 {
		idle = DefaultFetchIdleTimeout
	}
	return newTimeoutBody(ctx, cancel, body, idle)
}

// closeGrace bounds how long Close waits for the body pump to stop. A
// well-behaved body ends as soon as its request context is canceled; the grace
// exists so a driver that ignores cancellation cannot hang the read path.
const closeGrace = time.Second

// contextGrace bounds how long a canceled read waits for the body to report
// its own error before surfacing the context error.
const contextGrace = 100 * time.Millisecond

type timeoutBodyChunk struct {
	data []byte
	err  error
}

// timeoutBody turns a provider body into a reader that can be abandoned when
// the idle bound fires. The body read runs in one goroutine so a blocked Read
// cannot pin the caller; canceling the request and closing the body is what
// makes that goroutine finish.
type timeoutBody struct {
	ctx     context.Context
	cancel  context.CancelFunc
	body    io.ReadCloser
	idle    time.Duration
	started time.Time
	// totalLimit is the context deadline distance captured at construction.
	// It only feeds the timeout error's diagnostic fields.
	totalLimit time.Duration

	chunks chan timeoutBodyChunk
	stop   chan struct{}
	done   chan struct{}
	timer  *time.Timer

	pending    []byte
	pendingErr error
	err        error

	releaseOnce sync.Once
}

func newTimeoutBody(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, idle time.Duration) *timeoutBody {
	r := &timeoutBody{
		ctx:     ctx,
		cancel:  cancel,
		body:    body,
		idle:    idle,
		started: time.Now(),
		chunks:  make(chan timeoutBodyChunk, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		timer:   time.NewTimer(idle),
	}
	if deadline, ok := ctx.Deadline(); ok {
		r.totalLimit = time.Until(deadline)
	}
	go r.pump()
	return r
}

// pump reads the provider body and hands chunks to Read. It exits on EOF, a
// body error, or release.
func (r *timeoutBody) pump() {
	defer close(r.done)
	defer close(r.chunks)
	for {
		// Allocate per chunk instead of reusing one buffer: the caller owns
		// the bytes after handoff, so this saves a full copy of the window.
		buf := make([]byte, 64<<10)
		n, err := r.body.Read(buf)
		if n > 0 {
			select {
			case r.chunks <- timeoutBodyChunk{data: buf[:n], err: err}:
			case <-r.stop:
				return
			}
		} else if err != nil {
			select {
			case r.chunks <- timeoutBodyChunk{err: err}:
			case <-r.stop:
			}
			return
		}
		if err != nil {
			return
		}
	}
}

func (r *timeoutBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			return n, nil
		}
		if r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			r.err = err
			return 0, err
		}
		if r.err != nil {
			return 0, r.err
		}
		select {
		case chunk, ok := <-r.chunks:
			if !ok {
				r.timer.Stop()
				r.err = io.EOF
				return 0, io.EOF
			}
			r.pending = chunk.data
			r.pendingErr = chunk.err
			if len(chunk.data) > 0 {
				r.resetIdle()
			}
		case <-r.timer.C:
			return 0, r.abort(&FetchTimeoutError{
				Kind:    FetchTimeoutIdle,
				Limit:   r.idle,
				Elapsed: time.Since(r.started),
			})
		case <-r.ctx.Done():
			// Give the body a brief chance to report its own error so
			// driver-facing diagnostics record the abort instead of a clean
			// close, then surface the context error.
			select {
			case <-r.chunks:
			case <-time.After(contextGrace):
			}
			return 0, r.abort(r.contextError())
		}
	}
}

func (r *timeoutBody) resetIdle() {
	if !r.timer.Stop() {
		select {
		case <-r.timer.C:
		default:
		}
	}
	r.timer.Reset(r.idle)
}

func (r *timeoutBody) abort(err error) error {
	r.err = err
	r.release()
	return err
}

func (r *timeoutBody) contextError() error {
	err := r.ctx.Err()
	if errors.Is(err, context.DeadlineExceeded) {
		limit := r.totalLimit
		if limit <= 0 {
			limit = time.Since(r.started)
		}
		return &FetchTimeoutError{Kind: FetchTimeoutTotal, Limit: limit, Elapsed: time.Since(r.started)}
	}
	return err
}

// Close abandons the body: it cancels the request, closes the body, and waits
// briefly for the pump to stop.
func (r *timeoutBody) Close() error {
	r.releaseOnce.Do(func() {
		close(r.stop)
		r.timer.Stop()
		if r.cancel != nil {
			r.cancel()
		}
		_ = r.body.Close()
		select {
		case <-r.done:
		case <-time.After(closeGrace):
		}
	})
	return nil
}

// finish ends the watchdog after a complete read without closing the body. The
// caller that opened the body remains responsible for closing it, so a driver
// whose Close is not idempotent (os.File) sees exactly one Close.
func (r *timeoutBody) finish() {
	r.releaseOnce.Do(func() {
		close(r.stop)
		r.timer.Stop()
		if r.cancel != nil {
			r.cancel()
		}
	})
}

func (r *timeoutBody) release() {
	r.releaseOnce.Do(func() {
		close(r.stop)
		r.timer.Stop()
		if r.cancel != nil {
			r.cancel()
		}
		_ = r.body.Close()
		select {
		case <-r.done:
		case <-time.After(closeGrace):
		}
	})
}
