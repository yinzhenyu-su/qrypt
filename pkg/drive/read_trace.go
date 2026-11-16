package drive

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// ReadTrace collects actual execution counters for one logical window. It is
// separate from the immutable plan and contains no provider URLs or headers.
type ReadTrace struct {
	// ObserveRange is an optional concurrent sink, installed before use. It is
	// called outside source/scheduler locks; the execution layer does not log.
	ObserveRange func(ReadRangeEvent)
	mu           sync.Mutex
	extra        map[string]any
	active, peak int
	progressEnd  int64
	progressAt   time.Time
}

// ReadRangeEvent observes one admitted driver call, including opening and body
// lifetime. IDs are unique within SourceID; provider-private calls are excluded.
type ReadRangeEvent struct {
	SourceID     string
	ID           uint64
	Offset, Size int64
	Speculative  bool
	InFlight     int32
	StartedAt    time.Time
	Finished     bool
}

func (t *ReadTrace) observeRange(event ReadRangeEvent) {
	if t != nil && t.ObserveRange != nil {
		t.ObserveRange(event)
	}
}

// bodyProgress records plaintext bytes returned by the wrapped driver, including
// incomplete attempts. It lets a waiting consumer distinguish a stalled body
// from a progressing window that has not yet been validated and published.
func (t *ReadTrace) bodyProgress(n int, offset, received int64, started time.Time) {
	if t == nil || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.extra == nil {
		t.extra = map[string]any{}
	}
	if _, ok := t.extra["first_body_us"]; !ok {
		t.extra["first_body_us"] = time.Since(started).Microseconds()
	}
	if _, ok := t.extra["first_chunk_us"]; !ok && received >= ReadChunkSize {
		t.extra["first_chunk_us"] = time.Since(started).Microseconds()
	}
	if end := offset + received; end > t.progressEnd {
		t.progressEnd, t.progressAt = end, time.Now()
	}
	old, _ := t.extra["body_bytes"].(int64)
	t.extra["body_bytes"] = old + int64(n)
}

// BodyProgress returns the furthest received plaintext offset and the time it
// advanced. Replayed bytes count as traffic but do not renew progress. It is a
// contiguous frontier only for single-stream windows (including RunChunks);
// parallel windows may contain holes and must not use it to decide readiness.
// Progress never substitutes for validated block delivery.
func (t *ReadTrace) BodyProgress() (end int64, advanced time.Time) {
	if t == nil {
		return 0, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.progressEnd, t.progressAt
}

type readTraceKey struct{}

func WithReadTrace(ctx context.Context, trace *ReadTrace) context.Context {
	return context.WithValue(ctx, readTraceKey{}, trace)
}
func (t *ReadTrace) record(extra map[string]any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.extra == nil {
		t.extra = map[string]any{}
	}
	for k, v := range extra {
		switch k {
		case "granted_streams", "window_bytes", "part_bytes":
			t.extra[k] = v
			continue
		}
		switch n := v.(type) {
		case int:
			old, _ := t.extra[k].(int)
			t.extra[k] = old + n
		case int64:
			old, _ := t.extra[k].(int64)
			t.extra[k] = old + n
		default:
			t.extra[k] = v
		}
	}
}
func (t *ReadTrace) Snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]any, len(t.extra)+2)
	for k, v := range t.extra {
		out[k] = v
	}
	out["actual_in_flight"] = t.peak
	// body_bytes is cumulative traffic and counts replayed bytes; progress_end
	// is the furthest received offset. A consumer that needs the received
	// prefix (rather than the traffic volume) must use progress_end, and only
	// for single-stream windows.
	//
	// The traffic count is always published, as zero when the body never
	// started: a window whose row omits it is indistinguishable from one whose
	// traffic was never observed, and the two must not be confused.
	if _, ok := out["body_bytes"]; !ok {
		out["body_bytes"] = int64(0)
	}
	if t.progressEnd > 0 {
		out["progress_end"] = t.progressEnd
	}
	return out
}

func (t *ReadTrace) beginRange() func() {
	if t == nil {
		return func() {}
	}
	t.mu.Lock()
	t.active++
	t.peak = max(t.peak, t.active)
	t.mu.Unlock()
	return func() { t.mu.Lock(); t.active--; t.mu.Unlock() }
}
func (t *ReadTrace) failure(ctx context.Context, err error) {
	if t == nil {
		return
	}
	var timeout *FetchTimeoutError
	var network net.Error
	reason := ""
	switch {
	case errors.Is(context.Cause(ctx), ReadSuperseded):
		reason = string(ReadSuperseded)
	case ctx.Err() != nil || errors.Is(err, ErrReadSourceClosed):
		reason = string(ReadCallerCanceled)
	case errors.As(err, &timeout):
		if timeout.Kind == FetchTimeoutIdle {
			reason = string(ReadIdleStall)
		} else {
			reason = string(ReadTotalDeadline)
		}
	case errors.Is(err, io.ErrUnexpectedEOF):
		reason = string(ReadTruncated)
	case errors.As(err, &network):
		reason = string(ReadNetworkFailure)
	}
	if reason != "" {
		t.record(map[string]any{"failure_reason": reason})
	}
}
