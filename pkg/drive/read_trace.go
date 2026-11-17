package drive

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// fetchAttemptStats are the typed execution counters of one Range attempt.
type fetchAttemptStats struct {
	bodyUS      int64
	localWaitUS int64
	closeUS     int64
	validateUS  int64
}

// fetchStats are the typed retry counters and facts of one window fetch.
type fetchStats struct {
	attempts    int
	timeouts    int
	timeoutKind FetchTimeoutKind
	retryReason string
}

// readTraceStats accumulates one window's execution counters. The trace keeps
// them typed and renders the diagnostic map only in Snapshot, so execution
// code never keys a map to report what it did.
type readTraceStats struct {
	firstBodyUS  int64
	firstChunkUS int64
	bodyBytes    int64
	progressEnd  int64

	queueUS     int64
	openUS      int64
	bodyUS      int64
	localWaitUS int64
	closeUS     int64
	validateUS  int64
	deliverUS   int64

	grantedStreams int64
	windowBytes    int64
	partBytes      int64
	rangeTasks     int64

	fetchAttempts int64
	fetchTimeouts int64

	fetchTimeoutKind string
	fetchRetryReason string
	failureReason    string

	// Observed flags separate "this phase did not run" from "it ran in zero
	// time": the diagnostics keep the same keys the map accumulator emitted.
	attempted       bool
	queueObserved   bool
	openObserved    bool
	deliverObserved bool
}

// ReadTrace collects actual execution counters for one logical window. It is
// separate from the immutable plan and contains no provider URLs or headers.
type ReadTrace struct {
	// ObserveRange is an optional concurrent sink, installed before use. It is
	// called outside source/scheduler locks; the execution layer does not log.
	ObserveRange func(ReadRangeEvent)
	mu           sync.Mutex
	stats        readTraceStats
	active, peak int
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
	if t.stats.firstBodyUS == 0 {
		t.stats.firstBodyUS = time.Since(started).Microseconds()
	}
	if t.stats.firstChunkUS == 0 && received >= ReadChunkSize {
		t.stats.firstChunkUS = time.Since(started).Microseconds()
	}
	if end := offset + received; end > t.stats.progressEnd {
		t.stats.progressEnd, t.progressAt = end, time.Now()
	}
	t.stats.bodyBytes += int64(n)
}

// BodyProgress returns the furthest received plaintext offset and the time it
// advanced. Replayed bytes count as traffic but do not renew progress. It is a
// contiguous frontier only for single-stream windows (including FetchStream);
// parallel windows may contain holes and must not use it to decide readiness.
// Progress never substitutes for validated block delivery.
func (t *ReadTrace) BodyProgress() (end int64, advanced time.Time) {
	if t == nil {
		return 0, time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stats.progressEnd, t.progressAt
}

type readTraceKey struct{}

func WithReadTrace(ctx context.Context, trace *ReadTrace) context.Context {
	return context.WithValue(ctx, readTraceKey{}, trace)
}

// recordQueue and its siblings add the typed counters the execution layer
// reports. Only Snapshot renders them as diagnostic keys.
func (t *ReadTrace) recordQueue(d time.Duration) {
	t.recordTimings(func(s *readTraceStats) { s.queueUS += d.Microseconds(); s.queueObserved = true })
}

func (t *ReadTrace) recordOpen(d time.Duration) {
	t.recordTimings(func(s *readTraceStats) { s.openUS += d.Microseconds(); s.openObserved = true })
}

func (t *ReadTrace) recordDeliver(d time.Duration) {
	t.recordTimings(func(s *readTraceStats) { s.deliverUS += d.Microseconds(); s.deliverObserved = true })
}

func (t *ReadTrace) recordTimings(apply func(*readTraceStats)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	apply(&t.stats)
}

// recordAttempt adds one Range attempt's timings.
func (t *ReadTrace) recordAttempt(a fetchAttemptStats) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stats.attempted = true
	t.stats.bodyUS += a.bodyUS
	t.stats.localWaitUS += a.localWaitUS
	t.stats.closeUS += a.closeUS
	t.stats.validateUS += a.validateUS
}

// recordFetch adds one window fetch's retry counters. A reason or timeout kind
// without retries is not reported, matching the previous diagnostics.
func (t *ReadTrace) recordFetch(f fetchStats) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if f.attempts > 1 {
		t.stats.fetchAttempts += int64(f.attempts)
		t.stats.fetchRetryReason = f.retryReason
	}
	if f.timeouts > 0 {
		t.stats.fetchTimeouts += int64(f.timeouts)
		t.stats.fetchTimeoutKind = string(f.timeoutKind)
	}
}

// recordPlan publishes the executed shape of the window. These are the only
// facts that replace rather than accumulate: a window has one shape.
func (t *ReadTrace) recordPlan(grantedStreams int, rangeTasks int64, windowBytes, partBytes int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stats.grantedStreams = int64(grantedStreams)
	t.stats.rangeTasks += rangeTasks
	t.stats.windowBytes = windowBytes
	t.stats.partBytes = partBytes
}

func (t *ReadTrace) Snapshot() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]any, 17)
	if t.stats.queueObserved {
		out["queue_us"] = t.stats.queueUS
	}
	if t.stats.openObserved {
		out["open_us"] = t.stats.openUS
	}
	if t.stats.attempted {
		out["body_us"] = t.stats.bodyUS
		out["local_wait_us"] = t.stats.localWaitUS
		out["close_us"] = t.stats.closeUS
		out["validate_us"] = t.stats.validateUS
	}
	if t.stats.deliverObserved {
		out["deliver_us"] = t.stats.deliverUS
	}
	if t.stats.firstBodyUS > 0 {
		out["first_body_us"] = t.stats.firstBodyUS
	}
	if t.stats.firstChunkUS > 0 {
		out["first_chunk_us"] = t.stats.firstChunkUS
	}
	if t.stats.grantedStreams > 0 {
		// Stream and task counts stay int: they are counts, not durations, and
		// the diagnostics consumer formats them as integers.
		out["granted_streams"] = int(t.stats.grantedStreams)
		out["window_bytes"] = t.stats.windowBytes
		out["part_bytes"] = t.stats.partBytes
	}
	if t.stats.rangeTasks > 0 {
		out["range_tasks"] = int(t.stats.rangeTasks)
	}
	if t.stats.fetchAttempts > 0 {
		out["fetch_attempts"] = int(t.stats.fetchAttempts)
		out["fetch_retry_reason"] = t.stats.fetchRetryReason
	}
	if t.stats.fetchTimeouts > 0 {
		out["fetch_timeouts"] = int(t.stats.fetchTimeouts)
		out["fetch_timeout_kind"] = t.stats.fetchTimeoutKind
	}
	if t.stats.failureReason != "" {
		out["failure_reason"] = t.stats.failureReason
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
	out["body_bytes"] = t.stats.bodyBytes
	if t.stats.progressEnd > 0 {
		out["progress_end"] = t.stats.progressEnd
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
		t.mu.Lock()
		t.stats.failureReason = reason
		t.mu.Unlock()
	}
}
