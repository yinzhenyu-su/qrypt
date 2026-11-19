package simread

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// PaceSliceBytes bounds how many body bytes a single Read returns while a
// simulated rate limit is active. It only controls the granularity of pacing;
// the bytes delivered and the total simulated duration are unchanged.
const PaceSliceBytes = 32 << 10

// Fault is one scripted range-request outcome.
//
// Attempts are counted per (offset, size) range, so a retried range walks the
// fault list in order and a range with no remaining faults behaves normally.
type Fault struct {
	// Status fails the attempt before any byte is delivered, with a
	// [drive.HTTPStatusError]. 429 and 408 are classified as rate limits and
	// request timeouts by the production retry logic; 5xx is retried as a
	// status failure.
	Status int
	// Truncate delivers only half of the requested bytes and then a clean EOF,
	// so the production read path detects a short body and replays the range.
	Truncate bool
}

// Stall simulates a mid-body network stall: after AfterBytes bytes have been
// delivered the body pauses for For without advancing or erroring.
type Stall struct {
	AfterBytes int64
	For        time.Duration
}

// Config configures a simulated provider. Durations and rates are simulated
// values: the provider sleeps duration/TimeScale and delivers at
// RateBytesPerSec×TimeScale real bytes per second. The zero value passes reads
// straight through to the wrapped driver.
type Config struct {
	// TimeScale compresses simulated time into real time. Zero means 1 (real
	// time). A value of 2 makes a 150 KiB/s provider observe roughly 300 KiB/s
	// to the production read path.
	TimeScale float64
	// RateBytesPerSec is the per-connection simulated throughput. Zero means
	// unthrottled.
	RateBytesPerSec int64
	// OpenLatency delays the response to a Read call (simulated duration).
	OpenLatency time.Duration
	// TTFB delays the first body byte after the body was opened (simulated
	// duration).
	TTFB time.Duration
	// Stall pauses the body once after it has delivered Stall.AfterBytes.
	Stall Stall
	// MaxConnections caps concurrent open bodies (and thus concurrent range
	// attempts). Zero means unlimited.
	MaxConnections int
	// Faults scripts failures per (offset, size) attempt, starting at the first
	// attempt.
	Faults []Fault
	// Gate blocks the first body byte until the channel is closed. A nil
	// channel or nil func means no gating. It is the hook tests use to hold a
	// range in flight while they assert on the read layer.
	Gate func(offset, size int64) <-chan struct{}
}

// Request is one recorded Read attempt against the simulated provider.
type Request struct {
	Offset int64
	Size   int64
	// Attempt is the 1-based attempt number for the (Offset, Size) range.
	Attempt int
	// Status is the scripted HTTP status, zero for a normal attempt.
	Status int
	// Truncated reports whether the attempt was scripted to deliver a short
	// body.
	Truncated bool
	// Delivered is the number of body bytes the consumer received.
	Delivered int64
	// Concurrent is the number of attempts in flight when this one started,
	// including itself.
	Concurrent int
	// Started and Finished bracket the attempt. Finished is zero while the
	// attempt is still in flight.
	Started  time.Time
	Finished time.Time
	// Err is the attempt error, empty on success.
	Err string
}

// Stats is a snapshot of provider-wide counters.
type Stats struct {
	// Requests is the number of Read attempts recorded.
	Requests int
	// BytesServed is the body bytes delivered by finished attempts.
	BytesServed int64
	// MaxConcurrent is the peak number of in-flight attempts.
	MaxConcurrent int
	// Active is the current number of in-flight attempts.
	Active int
}

// Provider wraps a real driver and serves reads on a simulated timeline.
type Provider struct {
	drive.Driver

	cfg   Config
	scale float64
	slots chan struct{}

	mu            sync.Mutex
	attempts      map[rangeKey]int
	records       []*Request
	active        int
	maxConcurrent int
	bytesServed   int64
}

type rangeKey struct{ offset, size int64 }

// Wrap returns a simulated provider over base. It panics if base is nil.
func Wrap(base drive.Driver, cfg Config) *Provider {
	if base == nil {
		panic("simread: nil driver")
	}
	p := &Provider{
		Driver:   base,
		cfg:      cfg,
		scale:    cfg.TimeScale,
		attempts: make(map[rangeKey]int),
	}
	if p.scale <= 0 {
		p.scale = 1
	}
	if cfg.MaxConnections > 0 {
		p.slots = make(chan struct{}, cfg.MaxConnections)
	}
	return p
}

// Capabilities forwards the wrapped driver's capabilities and adds
// [drive.CapabilityParallelRange], so the production read policy allows
// parallel range plans against the simulated provider.
func (p *Provider) Capabilities() []drive.Capability {
	caps := p.Driver.Capabilities()
	for _, c := range caps {
		if c == drive.CapabilityParallelRange {
			return caps
		}
	}
	return append(append([]drive.Capability{}, caps...), drive.CapabilityParallelRange)
}

// ReadPolicy forwards the wrapped driver's policy when it declares one, and
// otherwise returns the production default policy.
func (p *Provider) ReadPolicy() drive.ReadPolicy {
	if provider, ok := p.Driver.(drive.ReadPolicyProvider); ok {
		return provider.ReadPolicy()
	}
	return drive.DefaultReadPolicy()
}

// Read serves one range through the simulated timeline.
func (p *Provider) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, err
	}
	rec := &Request{Offset: offset, Size: size, Started: time.Now()}
	p.mu.Lock()
	key := rangeKey{offset: offset, size: size}
	rec.Attempt = p.attempts[key] + 1
	p.attempts[key] = rec.Attempt
	p.active++
	rec.Concurrent = p.active
	if p.active > p.maxConcurrent {
		p.maxConcurrent = p.active
	}
	p.records = append(p.records, rec)
	p.mu.Unlock()

	fail := func(err error) (io.ReadCloser, error) {
		p.finish(rec, 0, err)
		p.release()
		return nil, err
	}
	if err := p.sleep(ctx, p.cfg.OpenLatency); err != nil {
		return fail(err)
	}
	if fault := p.fault(rec.Attempt); fault != nil && fault.Status != 0 {
		rec.Status = fault.Status
		return fail(&drive.HTTPStatusError{
			Status: fault.Status,
			Err:    fmt.Errorf("simread: scripted status %d", fault.Status),
		})
	}
	base, err := p.Driver.Read(ctx, e, offset, size)
	if err != nil {
		return fail(err)
	}
	limit := int64(-1)
	if fault := p.fault(rec.Attempt); fault != nil && fault.Truncate {
		rec.Truncated = true
		limit = size / 2
		if limit < 1 {
			limit = 1
		}
	}
	return &body{p: p, base: base, ctx: ctx, rec: rec, limit: limit}, nil
}

// Requests returns a snapshot of every recorded attempt, in start order.
func (p *Provider) Requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Request, len(p.records))
	for i, rec := range p.records {
		out[i] = *rec
	}
	return out
}

// Stats returns a snapshot of the provider counters.
func (p *Provider) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		Requests:      len(p.records),
		BytesServed:   p.bytesServed,
		MaxConcurrent: p.maxConcurrent,
		Active:        p.active,
	}
}

// Reset clears the request log and counters. Call it only when no attempt is
// in flight.
func (p *Provider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts = make(map[rangeKey]int)
	p.records = nil
	p.maxConcurrent = 0
	p.bytesServed = 0
}

func (p *Provider) fault(attempt int) *Fault {
	if attempt < 1 || attempt > len(p.cfg.Faults) {
		return nil
	}
	fault := p.cfg.Faults[attempt-1]
	return &fault
}

func (p *Provider) finish(rec *Request, delivered int64, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec.Delivered = delivered
	rec.Finished = time.Now()
	if err != nil {
		rec.Err = err.Error()
	}
	p.active--
	p.bytesServed += delivered
}

func (p *Provider) acquire(ctx context.Context) error {
	if p.slots == nil {
		return nil
	}
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Provider) release() {
	if p.slots != nil {
		<-p.slots
	}
}

// sleep waits for a simulated duration, scaled to real time.
func (p *Provider) sleep(ctx context.Context, simulated time.Duration) error {
	return sleepCtx(ctx, p.scaled(simulated))
}

// pace waits for the simulated time n bytes take at rate.
func (p *Provider) pace(ctx context.Context, n int, rate int64) error {
	if rate <= 0 || n <= 0 {
		return nil
	}
	simulated := time.Duration(float64(n) * float64(time.Second) / float64(rate))
	return sleepCtx(ctx, p.scaled(simulated))
}

func (p *Provider) scaled(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(float64(d) / p.scale)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
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
