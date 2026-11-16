package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// stalledTestBody blocks until its context is canceled, like a provider
// connection that accepted the request and stopped sending.
type stalledTestBody struct {
	ctx    context.Context
	closed atomic.Int32
}

func (b *stalledTestBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *stalledTestBody) Close() error {
	b.closed.Add(1)
	return nil
}

// pacedTestBody emits a fixed chunk every interval until total bytes or EOF.
type pacedTestBody struct {
	ctx       context.Context
	remaining int
	chunk     int
	interval  time.Duration
	closed    atomic.Int32
}

func (b *pacedTestBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	if b.interval > 0 {
		timer := time.NewTimer(b.interval)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	n := min(len(p), b.chunk, b.remaining)
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	b.remaining -= n
	return n, nil
}

func (b *pacedTestBody) Close() error {
	b.closed.Add(1)
	return nil
}

// trickleTestBody emits one byte per interval forever.
type trickleTestBody struct {
	ctx      context.Context
	interval time.Duration
	closed   atomic.Int32
}

func (b *trickleTestBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	timer := time.NewTimer(b.interval)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	p[0] = 'y'
	return 1, nil
}

func (b *trickleTestBody) Close() error {
	b.closed.Add(1)
	return nil
}

func fetchTimeoutCtx(t *testing.T, total time.Duration) (context.Context, context.CancelCauseFunc) {
	t.Helper()
	parent, cancelDeadline := context.WithTimeoutCause(context.Background(), total, ReadIdleStall)
	ctx, cancel := context.WithCancelCause(parent)
	return ctx, func(cause error) {
		cancel(cause)
		cancelDeadline()
	}
}

func TestReadBodyLimitedAbortsStalledBody(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
	defer cancel(nil)
	body := &stalledTestBody{ctx: ctx}
	started := time.Now()
	_, err := ReadBodyLimited(ctx, cancel, body, 1<<20, 50*time.Millisecond)
	if !IsFetchTimeout(err) {
		t.Fatalf("error = %v, want a fetch timeout", err)
	}
	var timeout *FetchTimeoutError
	if errors.As(err, &timeout) && timeout.Kind != FetchTimeoutIdle {
		t.Fatalf("timeout kind = %q, want idle", timeout.Kind)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took %s, want it bounded near 50ms", elapsed)
	}
	if body.closed.Load() != 1 {
		t.Fatalf("body closed %d times, want 1", body.closed.Load())
	}
	if ErrorCategory(err) != ErrorCategoryTimeout {
		t.Fatalf("category = %q, want %q", ErrorCategory(err), ErrorCategoryTimeout)
	}
}

func TestIdleTimeoutExcludesCompletedLocalWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(WithLocalWaitTracking(context.Background()))
		defer cancel()
		watch := localWaitWatchFrom(ctx)
		reader := newTimeoutBody(ctx, cancel, nil, &stalledTestBody{ctx: ctx}, 40*time.Millisecond)
		defer reader.Close()

		// Delay the consumer until after the local wait has ended and its
		// watchdog timer has expired. The provider then gets its full idle
		// allowance, even though the limiter is no longer actively waiting.
		watch.enter()
		time.Sleep(125 * time.Millisecond)
		watch.exit()
		started := time.Now()
		_, err := reader.Read(make([]byte, 1))
		if !IsFetchTimeout(err) {
			t.Fatalf("error = %v, want an idle timeout", err)
		}
		if elapsed := time.Since(started); elapsed != 40*time.Millisecond {
			t.Fatalf("provider idle time = %s, want 40ms", elapsed)
		}
	})
}

// TestIdleTimeoutDeliversQueuedChunkToLateConsumer covers a body that already
// produced a chunk while the consumer was not scheduled, which is what a loaded
// host does to a locally rate-limited read. Expiring the idle timer must then
// deliver that queued chunk instead of aborting: the provider produced the
// bytes, so the connection must not be reported as stalled and penalized.
func TestIdleTimeoutDeliversQueuedChunkToLateConsumer(t *testing.T) {
	const chunk = 64 << 10
	data := bytes.Repeat([]byte("b"), chunk)
	// The Read select races the buffered chunk against the expired timer, so a
	// single repetition could pass by luck; without the queued-chunk drain the
	// failure is the overwhelmingly likely outcome.
	for range 64 {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(WithLocalWaitTracking(context.Background()))
			defer cancel()
			limiter := NewBandwidthLimiter(BandwidthLimits{DownloadBytesPerSecond: 512 << 10})
			body := limiter.LimitDownload(ctx, io.NopCloser(bytes.NewReader(data)))
			reader := newTimeoutBody(ctx, cancel, nil, body, 40*time.Millisecond)
			defer reader.Close()

			// Stay out of Read past both the token wait (about 125 ms) and the
			// 40 ms idle timer, so the produced chunk is queued when the timer
			// has already fired.
			time.Sleep(200 * time.Millisecond)
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("idle bound aborted bytes the provider already sent: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("bytes = %d, want %d", len(got), len(data))
			}
		})
	}
}

func TestReadBodyLimitedAllowsSlowProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
		defer cancel(nil)
		body := &pacedTestBody{ctx: ctx, remaining: 8 * 1024, chunk: 1024, interval: 20 * time.Millisecond}
		data, err := ReadBodyLimited(ctx, cancel, body, 16*1024, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("ReadBodyLimited: %v", err)
		}
		if len(data) != 8*1024 {
			t.Fatalf("bytes = %d, want %d", len(data), 8*1024)
		}
		if body.closed.Load() != 0 {
			t.Fatalf("completed body was closed by ReadBodyLimited; the caller owns it")
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
		if body.closed.Load() != 1 {
			t.Fatalf("body closed %d times, want 1", body.closed.Load())
		}
	})
}

func TestReadBodyLimitedCapsTrickleWithTotalTimeout(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 100*time.Millisecond)
	defer cancel(nil)
	body := &trickleTestBody{ctx: ctx, interval: 10 * time.Millisecond}
	started := time.Now()
	_, err := ReadBodyLimited(ctx, cancel, body, 1<<20, time.Second)
	if !IsFetchTimeout(err) {
		t.Fatalf("error = %v, want a total fetch timeout", err)
	}
	var timeout *FetchTimeoutError
	if errors.As(err, &timeout) && timeout.Kind != FetchTimeoutTotal {
		t.Fatalf("timeout kind = %q, want total", timeout.Kind)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("total timeout took %s, want it bounded near 100ms", elapsed)
	}
}

// bodyDeadlineCtx reports an expired deadline without a Done channel, so the
// read can only learn about the abort from the body's own error. Real
// providers do report the request context's deadline from a pending read, and
// whether the read notices the context or the body first is a race.
type bodyDeadlineCtx struct{ context.Context }

func (bodyDeadlineCtx) Done() <-chan struct{} { return nil }
func (bodyDeadlineCtx) Err() error            { return context.DeadlineExceeded }

// failingTestBody fails every read with a fixed error.
type failingTestBody struct {
	err    error
	closed atomic.Int32
}

func (b *failingTestBody) Read([]byte) (int, error) { return 0, b.err }
func (b *failingTestBody) Close() error {
	b.closed.Add(1)
	return nil
}

// A total timeout the body reports itself must keep the timeout kind, so
// diagnostics and metrics still record a timeout rather than a bare context
// error.
func TestReadBodyLimitedClassifiesBodyReportedDeadline(t *testing.T) {
	ctx := bodyDeadlineCtx{Context: context.Background()}
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	body := &failingTestBody{err: context.DeadlineExceeded}
	_, err := ReadBodyLimited(ctx, cancel, body, 1<<20, time.Second)
	if !IsFetchTimeout(err) {
		t.Fatalf("error = %v, want a total fetch timeout", err)
	}
	var timeout *FetchTimeoutError
	if !errors.As(err, &timeout) || timeout.Kind != FetchTimeoutTotal {
		t.Fatalf("timeout = %+v, want kind %q", timeout, FetchTimeoutTotal)
	}
	if ErrorCategory(err) != ErrorCategoryTimeout {
		t.Fatalf("category = %q, want %q", ErrorCategory(err), ErrorCategoryTimeout)
	}
	if body.closed.Load() != 1 {
		t.Fatalf("body closed %d times, want 1", body.closed.Load())
	}
}

func TestReadBodyLimitedRejectsOverlongBody(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
	defer cancel(nil)
	body := io.NopCloser(bytes.NewReader(make([]byte, 11)))
	_, err := ReadBodyLimited(ctx, cancel, body, 10, time.Second)
	if err == nil || IsFetchTimeout(err) {
		t.Fatalf("error = %v, want a size error", err)
	}
}

func TestFetchTimeoutErrorUnwrapsDeadlineExceeded(t *testing.T) {
	err := &FetchTimeoutError{Kind: FetchTimeoutIdle}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("fetch timeout must unwrap to context.DeadlineExceeded")
	}
	if !RetryableCategory(ErrorCategory(err)) {
		t.Fatal("fetch timeout must be retryable")
	}
}
