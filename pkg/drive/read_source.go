package drive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
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
	Streams      int
	Split        SplitPolicy
	Timeouts     FetchTimeouts
	Priority     readbudget.Priority
	Speculative  bool
}

type readRange struct{ Offset, Size int64 }
type rangeLayout struct {
	offset, size, part int64
	workers            int
}

func partitionRange(offset, size int64, workers int, split SplitPolicy) rangeLayout {
	workers = max(1, workers)
	if size < split.MinParallelBytes {
		workers = 1
	}
	part := split.PartBytes
	if part == 0 {
		part = (size + int64(workers) - 1) / int64(workers)
	}
	return rangeLayout{offset: offset, size: size, part: part, workers: min(workers, int((size+part-1)/part))}
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
	mu        sync.Mutex
	closed    bool
	active    sync.WaitGroup
	bodies    map[*sourceReadBody]struct{}
	pending   map[*PendingRead]struct{}
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
	return &ReadSource{driver: d, policy: p, bodies: map[*sourceReadBody]struct{}{}, scheduler: readbudget.NewScheduler(readbudget.SchedulerConfig{MaxConcurrency: 32, HighReserve: 8, PrefetchLimit: 4}), ctx: ctx, cancel: cancel}, nil
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
	case <-time.After(closeGrace):
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
	if job, ok := ctx.Value(pendingReadKey{}).(*PendingRead); ok && job.source == s && job.entry.ID == e.ID {
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
	trace.record(map[string]any{"queue_us": time.Since(queueStarted).Microseconds()})
	if err := readCtx.Err(); err != nil {
		release()
		finish()
		return nil, err
	}
	openStarted := time.Now()
	body, err := s.driver.Read(readCtx, e, offset, size)
	trace.record(map[string]any{"open_us": time.Since(openStarted).Microseconds(), "driver_open_ms": durationMillis(time.Since(openStarted))})
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
// network request. Run consumes it once; Close also releases an unstarted job.
type PendingRead struct {
	source          *ReadSource
	entry           Entry
	plan            RangePlan
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
	plan.Streams = 1
	plan.Split = SplitPolicy{}
	plan.Speculative = true
	job := &PendingRead{source: s, entry: e, plan: plan, release: release}
	if s.pending == nil {
		s.pending = map[*PendingRead]struct{}{}
	}
	s.pending[job] = struct{}{}
	return job
}
func (p *PendingRead) Run(ctx context.Context) ([]byte, error) {
	return p.run(ctx, nil)
}

// RunChunks consumes an aligned speculative window in ReadChunkSize blocks.
// emit owns each immutable block and is called synchronously, in offset order.
// A later error applies only to the unconsumed tail; completed blocks remain
// valid. Retries request only the unpublished suffix. The last block is emitted
// only after response length and Close validation. Unknown-size objects retain
// whole-window validation. Run and RunChunks together may be called only once.
func (p *PendingRead) RunChunks(ctx context.Context, emit func(int64, []byte)) error {
	if emit == nil || p.plan.Offset%ReadChunkSize != 0 {
		return ErrInvalidInput
	}
	_, err := p.run(ctx, &rangeDelivery{next: p.plan.Offset, emit: emit})
	return err
}

func (p *PendingRead) run(ctx context.Context, delivery *rangeDelivery) ([]byte, error) {
	p.mu.Lock()
	if p.closed || p.started {
		p.mu.Unlock()
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithCancel(ctx)
	p.started = true
	p.cancel = cancel
	p.mu.Unlock()
	defer func() { cancel(); p.finish() }()
	return p.source.fetch(context.WithValue(ctx, pendingReadKey{}, p), p.entry, p.plan, delivery)
}
func (p *PendingRead) finish() {
	p.once.Do(func() { p.release(); p.source.mu.Lock(); delete(p.source.pending, p); p.source.mu.Unlock() })
}
func (p *PendingRead) Close() error {
	p.mu.Lock()
	p.closed = true
	started, cancel := p.started, p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !started {
		p.finish()
	}
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
	if plan.Split.PartBytes < 0 || plan.Split.PartBytes > MaxReadWindowBytes || plan.Split.MinParallelBytes < 0 || plan.Split.MinParallelBytes > MaxReadWindowBytes || plan.Streams < 0 || plan.Streams > 32 {
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
	workers := max(1, min(plan.Streams, s.policy.Effective.MaxInFlight))
	if !s.policy.Limits.Parallel || s.scheduler.IsNonParallel(e.ID) {
		workers = 1
	}
	if plan.Size < plan.Split.MinParallelBytes {
		workers = 1
	}
	// Compute tasks lazily. Even a one-byte ceiling creates at most workers
	// goroutines and one bounded output buffer, rather than millions of tasks.
	if e.Size <= 0 {
		workers = 1
		plan.Split.PartBytes = 0
	}
	workers = s.scheduler.GrantStreams(workers, e.ID, s.policy.Effective.MaxInFlight)
	layout := partitionRange(plan.Offset, plan.Size, workers, plan.Split)
	workers = layout.workers
	count := layout.count()
	strict := workers > 1
	if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
		trace.record(map[string]any{"granted_streams": workers, "range_tasks": int(count), "window_bytes": plan.Size, "part_bytes": layout.part})
	}
	if delivery != nil && e.Size > 0 {
		// Pending reads always have one admitted stream and no split policy.
		_, err := s.fetchPart(ctx, e, readRange{plan.Offset, plan.Size}, plan, delivery)
		if err != nil {
			if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
				trace.failure(ctx, err)
			}
		}
		return nil, err
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
			plan.Split.PartBytes = 0
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
	data, extra, err := fetchRange(ctx, e, span.Offset, span.Size, bounds, func(ctx context.Context, e Entry, offset, size int64) (io.ReadCloser, error) {
		return s.OpenRange(ctx, e, offset, size, p.Priority, p.Speculative)
	}, delivery)
	if trace, _ := ctx.Value(readTraceKey{}).(*ReadTrace); trace != nil {
		meta := make(map[string]any)
		for k, v := range extra {
			if strings.HasPrefix(k, "fetch_") {
				meta[k] = v
			}
		}

		trace.record(meta)
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
		data, err := r.source.Fetch(r.ctx, r.entry, RangePlan{Offset: r.offset, Size: n, Streams: policy.Bulk.Streams, Split: policy.Bulk.Split, Timeouts: policy.Fetch, Priority: readbudget.PriorityLow})
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
		trace.record(map[string]any{"deliver_us": time.Since(deliverStarted).Microseconds()})
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
