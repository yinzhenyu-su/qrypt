package read

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"go.uber.org/goleak"
)

// stalledReadBody blocks every read until the context is canceled, simulating
// a provider connection that stopped delivering bytes.
type stalledReadBody struct {
	ctx    context.Context
	closed atomic.Bool
}

func (b *stalledReadBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *stalledReadBody) Close() error {
	b.closed.Store(true)
	return nil
}

// pacedReadBody emits one chunk every interval until the span is exhausted.
// It is slow overall but never idle for long.
type pacedReadBody struct {
	ctx       context.Context
	remaining int
	chunk     int
	interval  time.Duration
}

func (b *pacedReadBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	timer := time.NewTimer(b.interval)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	n := min(len(p), b.chunk, b.remaining)
	for i := 0; i < n; i++ {
		p[i] = 'p'
	}
	b.remaining -= n
	return n, nil
}

func (b *pacedReadBody) Close() error { return nil }

// trickleReadBody emits one byte per interval and never finishes.
type trickleReadBody struct {
	ctx      context.Context
	interval time.Duration
}

func (b *trickleReadBody) Read(p []byte) (int, error) {
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
	p[0] = 't'
	return 1, nil
}

func (b *trickleReadBody) Close() error { return nil }

func newTimeoutScenarioReader(t *testing.T, size int, behavior drive.ReadBehavior) (*Reader, *State, *drive.FakeDriver) {
	t.Helper()
	reader, state, driver := newScenarioReader(t, size, behavior)
	reader.profile.FetchIdleTimeout = 50 * time.Millisecond
	reader.profile.FetchTotalTimeout = 5 * time.Second
	return reader, state, driver
}

func readError(t *testing.T, reader *Reader, ctx context.Context, offset, size int64) error {
	t.Helper()
	rc, err := reader.Read(ctx, "/file.bin", offset, size)
	if err == nil {
		if rc != nil {
			_, _ = io.Copy(io.Discard, rc)
			_ = rc.Close()
		}
		return nil
	}
	return err
}

func TestReaderReadStalledBodyTimesOut(t *testing.T) {
	reader, state, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	ctx := WithoutReadPrefetch(context.Background())
	started := time.Now()
	err := readError(t, reader, ctx, 0, ChunkSize)
	if !drive.IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("read returned after %s, want it bounded near the idle limit", elapsed)
	}
	stats := state.scheduler.Stats()
	if stats.NormalInUse != 0 || stats.HighInUse != 0 {
		t.Fatalf("slots after timeout: normal=%d high=%d, want 0", stats.NormalInUse, stats.HighInUse)
	}
	if fg, _, _ := state.TimeoutStats(); fg == 0 {
		t.Fatal("foreground timeout was not recorded")
	}
}

func TestReaderReadSlowProgressingBodySucceeds(t *testing.T) {
	reader, _, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, data []byte) io.ReadCloser {
			return &pacedReadBody{ctx: ctx, remaining: len(data), chunk: 1024, interval: 5 * time.Millisecond}
		},
	})
	data := readScenario(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if len(data) != ChunkSize {
		t.Fatalf("bytes = %d, want %d", len(data), ChunkSize)
	}
}

func TestReaderReadTricklingBodyHitsTotalCap(t *testing.T) {
	reader, _, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &trickleReadBody{ctx: ctx, interval: 5 * time.Millisecond}
		},
	})
	reader.profile.FetchIdleTimeout = time.Second
	reader.profile.FetchTotalTimeout = 80 * time.Millisecond
	err := readError(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if !drive.IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a total fetch timeout", err)
	}
}

func TestReaderWindowWaitersShareTimeoutError(t *testing.T) {
	reader, _, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	ctx := WithoutReadPrefetch(context.Background())
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			errs <- readError(t, reader, ctx, 0, ChunkSize)
		}()
	}
	deadline := time.After(3 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case err := <-errs:
			if !drive.IsFetchTimeout(err) {
				t.Fatalf("waiter error = %v, want the loader's timeout", err)
			}
		case <-deadline:
			t.Fatal("waiters did not return after the loader timed out")
		}
	}
	if calls := driver.ReadStats().Calls; calls != fetchTimeoutAttempts {
		t.Fatalf("driver calls = %d, want the waiters to share one load (one retry)", calls)
	}
}

func TestReaderRetriesTimedOutFetchOnce(t *testing.T) {
	var calls atomic.Int32
	reader, _, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, data []byte) io.ReadCloser {
			if calls.Add(1) == 1 {
				return &stalledReadBody{ctx: ctx}
			}
			return io.NopCloser(bytes.NewReader(data))
		},
	})
	data := readScenario(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if len(data) != ChunkSize {
		t.Fatalf("bytes = %d, want %d", len(data), ChunkSize)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("driver calls = %d, want the timed-out attempt plus one retry", got)
	}
}

func TestReaderRetryExhaustionFailsWholeWindow(t *testing.T) {
	reader, _, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	err := readError(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if !drive.IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	if calls := driver.ReadStats().Calls; calls != fetchTimeoutAttempts {
		t.Fatalf("driver calls = %d, want %d", calls, fetchTimeoutAttempts)
	}
}

func waitPrefetchDone(t *testing.T, state *State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		windows, prefetches, _ := state.RuntimeStats()
		if windows == 0 && prefetches == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("prefetch did not finish: windows=%d prefetches=%d", windows, prefetches)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPrefetchTimeoutIsSilentAndCounted(t *testing.T) {
	defer goleak.VerifyNone(t)
	obs := &recordingObserver{}
	reader, state, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	reader.observer = obs
	entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: 4 << 20, ModTime: time.Unix(1700000000, 0)}

	prefetchCtx := drive.WithDebugOperation(WithoutReadPrefetch(context.Background()), drive.DebugOperation{OpID: "op", Name: "/file.bin"})
	reader.prefetchWindow(prefetchCtx, entry, 1, 1)
	waitPrefetchDone(t, state)

	foreground, prefetch, last := state.TimeoutStats()
	if foreground != 0 || prefetch == 0 {
		t.Fatalf("timeout stats = foreground %d prefetch %d, want only prefetch", foreground, prefetch)
	}
	if last == nil || !last.Background || last.Kind != drive.FetchTimeoutIdle {
		t.Fatalf("last timeout = %+v, want a background idle timeout", last)
	}

	var reason string
	for _, detail := range obs.recordedDetails() {
		if detail.phase != "prefetch_window" {
			continue
		}
		if value, ok := detail.extra["prefetch_cancel_reason"].(string); ok {
			reason = value
		}
	}
	if reason != "timeout" {
		t.Fatalf("prefetch cancel reason = %q, want timeout", reason)
	}

	// The background failure must not affect a later foreground read.
	driver.ReadBehavior.BodyFunc = nil
	data := readScenario(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if len(data) != ChunkSize {
		t.Fatalf("foreground read = %d bytes, want %d", len(data), ChunkSize)
	}
}

func TestPrefetchCancelReasonNotOverwrittenByTimeout(t *testing.T) {
	obs := &recordingObserver{}
	reader, state, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	reader.observer = obs
	// A long idle bound leaves the invalidation as the only reason the
	// prefetch can end.
	reader.profile.FetchIdleTimeout = 30 * time.Second
	entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: 4 << 20, ModTime: time.Unix(1700000000, 0)}
	prefetchCtx := drive.WithDebugOperation(WithoutReadPrefetch(context.Background()), drive.DebugOperation{OpID: "op", Name: "/file.bin"})
	reader.prefetchWindow(prefetchCtx, entry, 1, 1)

	deadline := time.Now().Add(2 * time.Second)
	for driver.ReadStats().Calls == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prefetch never reached the driver")
		}
		time.Sleep(time.Millisecond)
	}
	state.InvalidateFile("cache")
	waitPrefetchDone(t, state)

	var reason string
	for _, detail := range obs.recordedDetails() {
		if detail.phase != "prefetch_window" {
			continue
		}
		if value, ok := detail.extra["prefetch_cancel_reason"].(string); ok {
			reason = value
		}
	}
	if reason != "invalidate" {
		t.Fatalf("prefetch cancel reason = %q, want invalidate", reason)
	}
}

func TestReaderStreamStalledBodyTimesOut(t *testing.T) {
	reader, _, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	rc, err := reader.ReadStream(WithoutReadPrefetch(context.Background()), "/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	buf := make([]byte, 1024)
	started := time.Now()
	_, err = rc.Read(buf)
	if !drive.IsFetchTimeout(err) {
		t.Fatalf("stream read error = %v, want a fetch timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("stream read returned after %s, want it bounded near the idle limit", elapsed)
	}
}

func TestForegroundReadTakesOverTimedOutPrefetch(t *testing.T) {
	var calls atomic.Int32
	reader, state, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, data []byte) io.ReadCloser {
			// The prefetch burns its original attempt and its retry; the
			// foreground read that takes over then succeeds.
			if calls.Add(1) <= fetchTimeoutAttempts {
				return &stalledReadBody{ctx: ctx}
			}
			return io.NopCloser(bytes.NewReader(data))
		},
	})
	prefetchCtx := drive.WithDebugOperation(WithoutReadPrefetch(context.Background()), drive.DebugOperation{OpID: "op", Name: "/file.bin"})
	entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: 4 << 20, ModTime: time.Unix(1700000000, 0)}
	reader.prefetchWindow(prefetchCtx, entry, 1, 1)

	deadline := time.Now().Add(2 * time.Second)
	for driver.ReadStats().Calls == 0 {
		if time.Now().After(deadline) {
			t.Fatal("prefetch never reached the driver")
		}
		time.Sleep(time.Millisecond)
	}

	// Reading the window the prefetch owns joins it, learns it timed out, and
	// loads the data itself instead of surfacing the background failure.
	ctx := WithoutReadPrefetch(context.Background())
	buf := make([]byte, ChunkSize)
	rc, err := reader.Read(ctx, "/file.bin", ChunkSize, ChunkSize)
	if err != nil {
		t.Fatalf("foreground read after prefetch timeout: %v", err)
	}
	defer rc.Close()
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("foreground read body: %v", err)
	}
	waitPrefetchDone(t, state)
}

func TestReadEventsRecordTimeoutReasonAndDurations(t *testing.T) {
	obs := &recordingObserver{}
	reader, _, _ := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
		BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
			return &stalledReadBody{ctx: ctx}
		},
	})
	reader.observer = obs
	err := readError(t, reader, WithoutReadPrefetch(context.Background()), 0, ChunkSize)
	if !drive.IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	extras := obs.recordedReadExtras()
	if len(extras) != 1 {
		t.Fatalf("summary events = %d, want 1", len(extras))
	}
	if reason, _ := extras[0]["cancel_reason"].(string); reason != "timeout" {
		t.Fatalf("cancel_reason = %q, want timeout", reason)
	}
	if _, ok := extras[0]["wait_us"]; !ok {
		t.Fatal("summary event is missing wait_us")
	}
	if fetchUS, ok := extras[0]["fetch_us"].(int64); !ok || fetchUS <= 0 {
		t.Fatalf("summary fetch_us = %v, want a positive duration", extras[0]["fetch_us"])
	}
	var sawFetchUS bool
	for _, detail := range obs.recordedDetails() {
		if detail.phase != "fetch_window" {
			continue
		}
		if _, ok := detail.extra["fetch_us"]; ok {
			sawFetchUS = true
		}
		if reason, _ := detail.extra["cancel_reason"].(string); reason != "timeout" {
			t.Fatalf("fetch_window cancel_reason = %q, want timeout", reason)
		}
	}
	if !sawFetchUS {
		t.Fatal("fetch_window event did not record fetch_us")
	}
}

func TestPrefetchCancelReasonsDoNotOverrideEachOther(t *testing.T) {
	stopReasons := []struct {
		name   string
		reason string
		stop   func(*State, context.Context)
	}{
		{
			name:   "invalidate",
			reason: "invalidate",
			stop:   func(state *State, _ context.Context) { state.InvalidateFile("cache") },
		},
		{
			name:   "seek",
			reason: "seek",
			stop:   func(state *State, _ context.Context) { state.cancelPrefetch("cache") },
		},
		{
			name:   "session release",
			reason: "session_release",
			stop:   func(state *State, _ context.Context) { state.ReleaseReadSession(7) },
		},
		{
			name:   "close",
			reason: "close",
			stop:   func(state *State, _ context.Context) { _ = state.Close() },
		},
	}
	for _, tc := range stopReasons {
		t.Run(tc.name, func(t *testing.T) {
			obs := &recordingObserver{}
			reader, state, driver := newTimeoutScenarioReader(t, 4<<20, drive.ReadBehavior{
				BodyFunc: func(ctx context.Context, _ []byte) io.ReadCloser {
					return &stalledReadBody{ctx: ctx}
				},
			})
			reader.observer = obs
			reader.profile.FetchIdleTimeout = 30 * time.Second
			ctx := WithAccessHint(WithoutReadPrefetch(context.Background()), AccessHint{SessionID: 7, RequestID: 1})
			ctx = drive.WithDebugOperation(ctx, drive.DebugOperation{OpID: "op", Name: "/file.bin"})
			entry := drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: 4 << 20, ModTime: time.Unix(1700000000, 0)}
			reader.prefetchWindow(ctx, entry, 1, 1)

			deadline := time.Now().Add(2 * time.Second)
			for driver.ReadStats().Calls == 0 {
				if time.Now().After(deadline) {
					t.Fatal("prefetch never reached the driver")
				}
				time.Sleep(time.Millisecond)
			}
			tc.stop(state, ctx)
			waitPrefetchDone(t, state)

			reason := ""
			for _, detail := range obs.recordedDetails() {
				if detail.phase != "prefetch_window" {
					continue
				}
				if value, ok := detail.extra["prefetch_cancel_reason"].(string); ok {
					reason = value
				}
			}
			if reason != tc.reason {
				t.Fatalf("prefetch cancel reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}
