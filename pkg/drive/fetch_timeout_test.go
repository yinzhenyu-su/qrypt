package drive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
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

func fetchTimeoutCtx(t *testing.T, total time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), total)
}

func TestReadBodyLimitedAbortsStalledBody(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
	defer cancel()
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

func TestReadBodyLimitedAllowsSlowProgress(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
	defer cancel()
	body := &pacedTestBody{ctx: ctx, remaining: 8 * 1024, chunk: 1024, interval: 10 * time.Millisecond}
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
}

func TestReadBodyLimitedCapsTrickleWithTotalTimeout(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 100*time.Millisecond)
	defer cancel()
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

func TestReadBodyLimitedRejectsOverlongBody(t *testing.T) {
	ctx, cancel := fetchTimeoutCtx(t, 5*time.Second)
	defer cancel()
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
