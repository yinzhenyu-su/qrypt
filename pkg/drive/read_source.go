package drive

import (
	testclock "github.com/yinzhenyu/qrypt/pkg/clock"

	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/readbudget"
	"golang.org/x/sync/errgroup"
)

var ErrReadAdmissionBusy = errors.New("read: no speculative capacity")
var readSourceSequence atomic.Uint64

var ErrReadSourceClosed = errors.New("read: source closed")

type ReadSourceProvider interface{ ReadSourceForPath(string) *ReadSource }
type MountReadSourceProvider interface{ ReadSourceForMount(string) *ReadSource }

type readBodyTransformKey struct{}
type ReadBodyTransform func(context.Context, Entry, int64, int64, io.ReadCloser) (io.ReadCloser, error)

func WithReadBodyTransform(ctx context.Context, f ReadBodyTransform) context.Context {
	return context.WithValue(ctx, readBodyTransformKey{}, f)
}

type RangePlan struct {
	Offset, Size int64
	// Streams requests a worker count without a part size; the caller that
	// knows the download shape sets Workers and Part explicitly instead. The
	// read layer translates driver split policy into an explicit layout, so the
	// source never interprets policy.
	Streams     int
	Workers     int
	Part        int64
	Timeouts    FetchTimeouts
	Priority    readbudget.Priority
	Speculative bool
}

type readRange struct{ Offset, Size int64 }
type rangeLayout struct {
	offset, size, part int64
	workers            int
}

func explicitRangeLayout(offset, size int64, workers int, part int64) rangeLayout {
	workers = max(1, workers)
	if part <= 0 {
		part = (size + int64(workers) - 1) / int64(workers)
	}
	return rangeLayout{offset: offset, size: size, part: part, workers: min(workers, int((size+part-1)/part))}
}

// WindowPartLayout translates a driver's split policy into the explicit part
// layout the source executes. Below MinParallelBytes the window stays on one
// stream; PartBytes (or an even split when zero) sizes each part. The read layer
// and the direct sourceStream both use this so policy is interpreted once.
func WindowPartLayout(size int64, streams int, partBytes, minParallelBytes int64) (workers int, part int64) {
	workers = max(1, streams)
	if minParallelBytes > 0 && size < minParallelBytes {
		return 1, 0
	}
	return workers, partBytes
}

func (l rangeLayout) count() int64 { return (l.size + l.part - 1) / l.part }
func (l rangeLayout) at(index int64) readRange {
	start := index * l.part
	return readRange{l.offset + start, min(l.part, l.size-start)}
}

// ReadSource owns a driver instance's read runtime. VFS and direct consumers
// share this object, rather than independently rediscovering driver identity.
type ReadSource struct {
	driver    Driver
	policy    ResolvedReadPolicy
	scheduler *readbudget.Scheduler
	ctx       context.Context
	cancel    context.CancelFunc
	// clock is the time seam for fetch retry backoff and close grace; it is
	// testclock.Real unless ReadOverride.Clock installs one.
	clock   testclock.Clock
	mu      sync.Mutex
	closed  bool
	active  sync.WaitGroup
	bodies  map[*sourceReadBody]struct{}
	pending map[*PendingRead]struct{}
	// Ranges count admitted driver calls through body EOF/Close, including
	// opening. Reserved but unstarted PendingReads are not active ranges.
	ranges  atomic.Int32
	rangeID atomic.Uint64
}

func NewReadSource(d Driver, o *ReadOverride) (*ReadSource, error) {
	if d == nil {
		return nil, ErrInvalidInput
	}
	p, err := ResolveReadPolicy(d, o)
	if err != nil {
		return nil, err
	}
	p.SourceID = fmt.Sprintf("read-%d", readSourceSequence.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
	clock := testclock.Real
	if o != nil && o.Clock != nil {
		clock = o.Clock
	}
	return &ReadSource{driver: d, policy: p, bodies: map[*sourceReadBody]struct{}{}, scheduler: readbudget.NewScheduler(readbudget.SchedulerConfig{MaxConcurrency: 32, HighReserve: 8, PrefetchLimit: 4}), ctx: ctx, cancel: cancel, clock: clock}, nil
}
func (s *ReadSource) Policy() ResolvedReadPolicy {
	p := s.policy
	p.Origins = make(map[string]string, len(s.policy.Origins))
	for k, v := range s.policy.Origins {
		p.Origins[k] = v
	}
	return p
}
func (s *ReadSource) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	bodies := make([]*sourceReadBody, 0, len(s.bodies))
	for b := range s.bodies {
		bodies = append(bodies, b)
	}
	pending := make([]*PendingRead, 0, len(s.pending))
	for job := range s.pending {
		pending = append(pending, job)
	}
	s.mu.Unlock()
	for _, job := range pending {
		job.Close()
	}
	for _, b := range bodies {
		go func() { _ = b.Close() }()
	}
	done := make(chan struct{})
	go func() { s.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-s.clock.After(closeGrace):
		return fmt.Errorf("read: source close timed out")
	}
}

// OpenRange admits one driver call and retains its complete permit until the
// body is closed or reaches EOF. Wrappers call the raw driver without another
// admission, so an encrypted source with a budget of one cannot self-deadlock.
func (s *ReadSource) OpenRange(ctx context.Context, e Entry, offset, size int64, p readbudget.Priority, speculative bool) (io.ReadCloser, error) {
	if e.ID == "" {
		return nil, fmt.Errorf("read: stable object ID is required: %w", ErrInvalidInput)
	}
	if offset < 0 || size < 0 || offset > math.MaxInt64-size {
		return nil, ErrInvalidInput
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrReadSourceClosed
	}
	s.active.Add(1)
	s.mu.Unlock()
	readCtx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.ctx, func() { cancel(ReadCallerCanceled) })
	finish := func() { stop(); cancel(nil); s.active.Done() }
	queueStarted := time.Now()
	trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace)
	var release func()
	var err error
	if job, ok := ctx.Value(pendingReadKey{}).(*PendingRead); ok && job.source == s && job.entry.ID == e.ID && job.active() {
		release = func() {}
	} else if speculative {
		var ok bool
		release, ok = s.scheduler.TryAcquireRange(e.ID, s.policy.Effective.MaxInFlight, p)
		if !ok {
			finish()
			return nil, ErrReadAdmissionBusy
		}
	} else {
		release, err = s.scheduler.AcquireRange(readCtx, e.ID, s.policy.Effective.MaxInFlight, p)
		if err != nil {
			finish()
			return nil, err
		}
	}
	endRange := trace.beginRange()
	rangeID := s.rangeID.Add(1)
	rangeStarted := time.Now()
	inFlight := s.ranges.Add(1)
	trace.observeRange(ReadRangeEvent{SourceID: s.policy.SourceID, ID: rangeID, Offset: offset, Size: size, Speculative: speculative, InFlight: inFlight, StartedAt: rangeStarted})
	permitRelease := release
	var releaseOnce sync.Once
	release = func() {
		releaseOnce.Do(func() {
			inFlight := s.ranges.Add(-1)
			trace.observeRange(ReadRangeEvent{SourceID: s.policy.SourceID, ID: rangeID, Offset: offset, Size: size, Speculative: speculative, InFlight: inFlight, StartedAt: rangeStarted, Finished: true})
			permitRelease()
			endRange()
		})
	}
	trace.recordQueue(time.Since(queueStarted))
	if err := readCtx.Err(); err != nil {
		release()
		finish()
		return nil, err
	}
	openStarted := time.Now()
	body, err := s.driver.Read(readCtx, e, offset, size)
	trace.recordOpen(time.Since(openStarted))
	if err != nil {
		release()
		finish()
		return nil, err
	}
	if body == nil {
		release()
		finish()
		return nil, fmt.Errorf("read: driver returned a nil body")
	}
	if transform, ok := ctx.Value(readBodyTransformKey{}).(ReadBodyTransform); ok {
		transformed, transformErr := transform(readCtx, e, offset, size, body)
		if transformErr != nil || transformed == nil {
			_ = body.Close()
			release()
			finish()
			if transformErr == nil {
				transformErr = fmt.Errorf("read: body transform returned a nil body")
			}
			return nil, transformErr
		}
		body = transformed
	}
	wrapped := &sourceReadBody{ReadCloser: body, release: release, remaining: size, bounded: size > 0, trace: trace, offset: offset, openedAt: openStarted, onTruncated: func() { cancel(ReadTruncated) }}
	wrapped.finish = func() { s.mu.Lock(); delete(s.bodies, wrapped); s.mu.Unlock(); release(); finish() }
	s.mu.Lock()
	if s.bodies == nil {
		s.bodies = map[*sourceReadBody]struct{}{}
	}
	s.bodies[wrapped] = struct{}{}
	closed := s.closed
	s.mu.Unlock()
	if closed {
		_ = wrapped.Close()
		return nil, ErrReadSourceClosed
	}
	return wrapped, nil
}

type sourceReadBody struct {
	io.ReadCloser
	once        sync.Once
	finish      func()
	closeErr    error
	bodyOnce    sync.Once
	release     func()
	remaining   int64
	bounded     bool
	readErr     error
	onTruncated func()
	trace       *ReadTrace
	offset      int64
	openedAt    time.Time
	received    int64
}

func (b *sourceReadBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	n, err := b.ReadCloser.Read(p)
	b.received += int64(n)
	b.trace.bodyProgress(n, b.offset, b.received, b.openedAt)
	b.remaining -= int64(n)
	if err == io.EOF && b.bounded && b.remaining > 0 {
		b.onTruncated()
	}
	if err != nil {
		b.readErr = err
		b.closeBody()
		// Keep admission through the executor's EOF/oversize check. Reaching
		// the requested byte count alone does not end the body's lifecycle.
		b.release()
	}
	return n, err
}
func (b *sourceReadBody) Close() error {
	b.once.Do(func() { b.closeBody(); b.finish() })
	return b.closeErr
}

func (b *sourceReadBody) closeBody() { b.bodyOnce.Do(func() { b.closeErr = b.ReadCloser.Close() }) }

// PendingRead reserves complete speculative admission without opening a
// network request. Context hands the reservation to the read layer, which
// executes the window through FetchStream or its own part workers; Close
// releases it and cancels any work running under it.
type PendingRead struct {
	source          *ReadSource
	entry           Entry
	release         func()
	once            sync.Once
	mu              sync.Mutex
	started, closed bool
	cancel          context.CancelFunc
}
type pendingReadKey struct{}

func (s *ReadSource) TryFetch(e Entry, plan RangePlan) *PendingRead {
	if e.ID == "" || plan.Offset < 0 || plan.Size <= 0 || plan.Size > MaxReadWindowBytes || plan.Offset > math.MaxInt64-plan.Size {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	release, ok := s.scheduler.TryAcquireRange(e.ID, s.policy.Effective.MaxInFlight, plan.Priority)
	if !ok {
		return nil
	}
	job := &PendingRead{source: s, entry: e, release: release}
	if s.pending == nil {
		s.pending = map[*PendingRead]struct{}{}
	}
	s.pending[job] = struct{}{}
	return job
}

// Context returns a context carrying the admission this job already holds, so
// Range requests executed with it reuse that permit instead of acquiring
// another one. The job must stay open until those requests finish; Close
// cancels them and releases the reservation. A job can serve one consumer
// only.
func (p *PendingRead) Context(ctx context.Context) (context.Context, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.started {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithCancel(ctx)
	p.started = true
	p.cancel = cancel
	return context.WithValue(ctx, pendingReadKey{}, p), nil
}

// active reports whether the job still holds its reservation.
func (p *PendingRead) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed
}

func (p *PendingRead) finish() {
	p.once.Do(func() { p.release(); p.source.mu.Lock(); delete(p.source.pending, p); p.source.mu.Unlock() })
}
func (p *PendingRead) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.finish()
	return nil
}

// Fetch validates one bounded window before returning any bytes. Every caller
// uses this same strict-range, timeout and truncated-body execution boundary.
func (s *ReadSource) Fetch(ctx context.Context, e Entry, plan RangePlan) ([]byte, error) {
	return s.fetch(ctx, e, plan, nil)
}

func (s *ReadSource) fetch(ctx context.Context, e Entry, plan RangePlan, delivery *rangeDelivery) ([]byte, error) {
	if e.ID == "" {
		return nil, fmt.Errorf("read: stable object ID is required: %w", ErrInvalidInput)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrReadSourceClosed
	}
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()
	if plan.Timeouts.Idle <= 0 {
		plan.Timeouts.Idle = s.policy.Effective.Fetch.Idle
	}
	if plan.Timeouts.Total <= 0 {
		plan.Timeouts.Total = s.policy.Effective.Fetch.Total
	}
	plan.Timeouts.Idle = min(plan.Timeouts.Idle, plan.Timeouts.Total)
	if plan.Streams < 0 || plan.Streams > 32 || plan.Workers < 0 || plan.Workers > 32 || plan.Part < 0 || plan.Part > MaxReadWindowBytes {
		return nil, ErrInvalidInput
	}
	if plan.Offset < 0 || plan.Size <= 0 || plan.Size > MaxReadWindowBytes || plan.Offset > math.MaxInt64-plan.Size {
		return nil, fmt.Errorf("read: invalid window: %w", ErrInvalidInput)
	}
	if e.Size > 0 {
		if plan.Offset >= e.Size {
			return []byte{}, nil
		}
		plan.Size = min(plan.Size, e.Size-plan.Offset)
	}
	if delivery != nil {
		// Progressive delivery is one streaming Range: the read layer owns
		// part fan-out (FetchPart per part), so a window delivery never opens
		// several connections of its own.
		if e.Size <= 0 || plan.Offset%ReadChunkSize != 0 {
			// Block boundaries are chunk aligned; an unaligned or unknown-size
			// window keeps the buffered contract and publishes once validated.
			data, err := s.fetch(ctx, e, plan, nil)
			if err != nil {
				if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
					trace.failure(ctx, err)
				}
				return nil, err
			}
			for len(data) > 0 {
				n := min(int64(len(data)), ReadChunkSize)
				delivery.publish(data[:n:n])
				data = data[n:]
			}
			return nil, nil
		}
		if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
			trace.recordPlan(1, 1, plan.Size, plan.Size)
		}
		_, err := s.fetchPart(ctx, e, readRange{plan.Offset, plan.Size}, plan, delivery)
		if err != nil {
			if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
				trace.failure(ctx, err)
			}
		}
		return nil, err
	}
	workers := plan.Streams
	if plan.Workers > 0 {
		// The read layer chose the part count for this window; keep its choice.
		workers = plan.Workers
	}
	workers = max(1, min(workers, s.policy.Effective.MaxInFlight))
	if !s.policy.Limits.Parallel || s.scheduler.IsNonParallel(e.ID) {
		workers = 1
	}
	// Compute tasks lazily. Even a one-byte ceiling creates at most workers
	// goroutines and one bounded output buffer, rather than millions of tasks.
	if e.Size <= 0 {
		workers = 1
		plan.Part = 0
	}
	workers = s.scheduler.GrantStreams(workers, e.ID, s.policy.Effective.MaxInFlight)
	layout := explicitRangeLayout(plan.Offset, plan.Size, workers, plan.Part)
	workers = layout.workers
	count := layout.count()
	strict := workers > 1
	if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
		trace.recordPlan(workers, count, plan.Size, layout.part)
	}
	data := make([]byte, int(plan.Size))
	var next atomic.Int64
	var short atomic.Int64
	short.Store(plan.Size)
	group, gctx := errgroup.WithContext(ctx)
	for range workers {
		group.Go(func() error {
			for {
				index := next.Add(1) - 1
				if index >= count {
					return nil
				}
				span := layout.at(index)
				start := span.Offset - plan.Offset
				partCtx := gctx
				if strict {
					partCtx = WithStrictRange(partCtx)
				}
				piece, err := s.fetchPart(partCtx, e, span, plan, nil)
				if err != nil {
					return err
				}
				copy(data[int(start):], piece)
				if int64(len(piece)) < span.Size {
					short.Store(start + int64(len(piece)))
					return nil
				}
			}
		})
	}
	if err := group.Wait(); err != nil {
		if strict && errors.Is(err, ErrUnsupported) {
			s.scheduler.MarkNonParallel(e.ID)
			plan.Streams = 1
			plan.Workers = 1
			plan.Part = 0
			return s.Fetch(ctx, e, plan)
		}
		if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
			trace.failure(ctx, err)
		}
		return nil, err
	}
	data = data[:short.Load()]
	if delivery != nil {
		for len(data) > 0 {
			n := min(ReadChunkSize, len(data))
			delivery.publish(data[:n:n])
			data = data[n:]
		}
		return nil, nil
	}
	return data, nil
}

func (s *ReadSource) fetchPart(ctx context.Context, e Entry, span readRange, p RangePlan, delivery *rangeDelivery) ([]byte, error) {
	bounds := p.Timeouts
	data, stats, err := fetchRange(ctx, e, span.Offset, span.Size, bounds, func(ctx context.Context, e Entry, offset, size int64) (io.ReadCloser, error) {
		return s.OpenRange(ctx, e, offset, size, p.Priority, p.Speculative)
	}, delivery)
	if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
		trace.recordFetch(stats)
	}
	return data, err
}

// Scheduler is the source-owned admission runtime shared with cache planning.
func (s *ReadSource) Scheduler() *readbudget.Scheduler { return s.scheduler }

// Read opens a bounded-memory bulk stream. Each refill is validated before
// output, so a short Range is never committed as a successful file segment.
func (s *ReadSource) Read(ctx context.Context, e Entry, offset, size int64) (io.ReadCloser, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrReadSourceClosed
	}
	if e.ID == "" || offset < 0 || size < 0 || offset > math.MaxInt64-size {
		return nil, ErrInvalidInput
	}
	if e.Size > 0 {
		if offset >= e.Size {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		if size == 0 {
			size = e.Size - offset
		} else {
			size = min(size, e.Size-offset)
		}
	}
	child, cancel := context.WithCancel(ctx)
	return &sourceStream{ctx: child, cancel: cancel, source: s, entry: e, offset: offset, remaining: size, known: size > 0}, nil
}

type sourceStream struct {
	ctx               context.Context
	cancel            context.CancelFunc
	source            *ReadSource
	entry             Entry
	offset, remaining int64
	known             bool
	buf               []byte
	done              bool
}

func (r *sourceStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(r.buf) == 0 {
		if r.done || r.known && r.remaining == 0 {
			return 0, io.EOF
		}
		policy := r.source.policy.Effective
		n := policy.Bulk.WindowBytes
		if r.known {
			n = min(n, r.remaining)
		}
		workers, part := WindowPartLayout(n, policy.Bulk.Streams, policy.Bulk.PartBytes, policy.Bulk.MinParallelBytes)
		data, err := r.source.Fetch(r.ctx, r.entry, RangePlan{Offset: r.offset, Size: n, Workers: workers, Part: part, Timeouts: policy.Fetch, Priority: readbudget.PriorityLow})
		if err != nil {
			return 0, err
		}
		if len(data) == 0 {
			return 0, io.EOF
		}
		r.buf = data
		r.done = int64(len(data)) < n
	}
	deliverStarted := time.Now()
	n := copy(p, r.buf)
	if trace, _ := r.ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
		trace.recordDeliver(time.Since(deliverStarted))
	}
	r.buf = r.buf[n:]
	r.offset += int64(n)
	if r.known {
		r.remaining -= int64(n)
	}
	return n, nil
}
func (r *sourceStream) Close() error { r.cancel(); r.buf = nil; r.done = true; return nil }

// classifyTimeoutError maps an expired fetch deadline to the timeout error
// semantic without hiding the caller's own cancellation.
func classifyTimeoutError(parent, fetch context.Context, err error, limit time.Duration) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if IsFetchTimeout(err) {
		return err
	}
	// HTTP response-header and driver-private timeouts can also unwrap to
	// DeadlineExceeded. Only our own expired context proves a fetch deadline.
	if errors.Is(fetch.Err(), context.DeadlineExceeded) {
		return &FetchTimeoutError{Kind: FetchTimeoutTotal, Limit: limit, Elapsed: limit}
	}
	return err
}
