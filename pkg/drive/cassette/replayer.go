package cassette

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// ErrNoRecord is returned when the read layer asks the replayer for a range the
// cassette does not contain. Replay never fabricates bytes: a missing record
// means the window shape diverged from the recording and the test must fail
// instead of silently measuring a different execution.
var ErrNoRecord = errors.New("cassette: no recorded request for range")

// ErrIncompleteRecord is returned when replay outlives a body that was cut
// short when it was recorded (the caller that recorded it closed it before
// EOF). A faithful replay always cancels at the same point; reaching this error
// means the replayed caller kept reading where the recorded one stopped.
var ErrIncompleteRecord = errors.New("cassette: recorded body was closed before EOF")

// requestKey identifies one logical Range request. Replay matches on the exact
// triple because a different offset or size is a different provider call.
type requestKey struct {
	entryID string
	offset  int64
	size    int64
}

// Replayer is a read-only drive.Driver backed by a cassette. It serves the
// recorded bodies with the recorded pacing scaled by a time factor and records
// what it actually ran, so a test can compare the replayed shape with the
// recording. Non-read driver methods are unsupported: a cassette describes
// reads, not a writable backend.
type Replayer struct {
	drive.UnsupportedOperations

	cassette *Cassette
	scale    float64

	mu            sync.Mutex
	pending       map[requestKey][]*RequestRecord
	files         map[string]FileRecord
	results       []ReplayResult
	requests      int
	bytes         int64
	providerBytes int64
	active        int
	peak          int
	misses        int
}

// ReplayOption configures a Replayer.
type ReplayOption func(*Replayer)

// WithTimeScale scales recorded durations into real time: replayed duration =
// recorded duration x scale, so 0.002 replays a four-second stall in eight
// milliseconds. A scale of zero disables pacing entirely, which is useful when a
// test only cares about bytes and request shape; the default is 1 (real time).
func WithTimeScale(scale float64) ReplayOption {
	return func(r *Replayer) { r.scale = scale }
}

// NewReplayer validates the cassette and prepares one replay queue per recorded
// range. It fails for a negative time scale; without an option replay runs at
// real time (scale 1).
func NewReplayer(c *Cassette, opts ...ReplayOption) (*Replayer, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	r := &Replayer{
		cassette: c,
		scale:    1,
		pending:  make(map[requestKey][]*RequestRecord),
		files:    c.FilesByID(),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.scale < 0 {
		return nil, fmt.Errorf("cassette: invalid time scale %v", r.scale)
	}
	for i := range c.Requests {
		record := &c.Requests[i]
		key := requestKey{entryID: record.EntryID, offset: record.Offset, size: record.Size}
		r.pending[key] = append(r.pending[key], record)
	}
	return r, nil
}

// Read serves one recorded Range request. It sleeps the recorded open latency,
// rebuilds a recorded open error when there was one, and otherwise returns a
// body that delivers the recorded chunks at the recorded pace.
func (r *Replayer) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	key := requestKey{entryID: entry.ID, offset: offset, size: size}

	r.mu.Lock()
	queue := r.pending[key]
	if len(queue) == 0 {
		r.misses++
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: entry %q offset %d size %d", ErrNoRecord, entry.ID, offset, size)
	}
	record := queue[0]
	r.pending[key] = queue[1:]
	r.requests++
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()

	result := &ReplayResult{
		EntryID: entry.ID,
		Offset:  offset,
		Size:    size,
		Attempt: record.Attempt,
	}
	opened := time.Now()
	if err := r.sleep(ctx, r.paced(record.OpenNS)); err != nil {
		result.Open = time.Since(opened)
		result.Err = err
		r.finish(result)
		return nil, err
	}
	result.Open = time.Since(opened)
	if err := replayError(record.ErrorKind, record.Status, record.Error); err != nil {
		result.Err = err
		r.finish(result)
		return nil, err
	}
	return &replayBody{
		ctx:      ctx,
		replayer: r,
		record:   record,
		result:   result,
		started:  time.Now(),
	}, nil
}

// Capabilities reports the union of the capabilities recorded for the cassette
// files, so the read layer resolves the same parallel-window policy it used
// while recording.
func (r *Replayer) Capabilities() []drive.Capability {
	seen := make(map[drive.Capability]struct{})
	var out []drive.Capability
	for _, file := range r.cassette.Files {
		for _, capability := range file.Capabilities {
			if _, ok := seen[capability]; ok {
				continue
			}
			seen[capability] = struct{}{}
			out = append(out, capability)
		}
	}
	return out
}

// Init is a no-op: replay owns no remote state.
func (r *Replayer) Init(context.Context) error { return nil }

// Drop is a no-op: replay must not delete anything.
func (r *Replayer) Drop(context.Context) error { return nil }

// List is unsupported: a cassette records read traffic, not a directory tree.
func (r *Replayer) List(context.Context, string) ([]drive.Entry, error) {
	return nil, drive.ErrUnsupported
}

// Space is unsupported: replay has no provider quota.
func (r *Replayer) Space(context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

// DebugSnapshot reports replay counters for diagnostics; it contains no
// provider URLs or credentials.
func (r *Replayer) DebugSnapshot(context.Context) (drive.DebugSnapshot, error) {
	stats := r.Stats()
	return drive.DebugSnapshot{
		Driver:      "cassette",
		Health:      "ok",
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"requests":       stats.Requests,
			"misses":         stats.Misses,
			"bytes":          stats.Bytes,
			"provider_bytes": stats.ProviderBytes,
		},
	}, nil
}

// Metrics returns no metric events: replay is a test double, not a provider.
func (r *Replayer) Metrics(context.Context, time.Time) ([]drive.MetricEvent, error) {
	return nil, nil
}

// ReplayResult is what one replayed Range request actually did: the open wait,
// the chunk delivery times relative to body hand-off, and the bytes returned.
// It mirrors RequestRecord so a test can compare the replay with the recording.
type ReplayResult struct {
	EntryID string
	Offset  int64
	Size    int64
	Attempt int
	// Open is the observed open wait (the replayed recorded OpenNS).
	Open time.Duration
	// Chunks are the observed chunk delivery times relative to body hand-off.
	Chunks []time.Duration
	// Bytes is how many bytes the caller consumed from the replayed body.
	Bytes int64
	// ProviderBytes is how many recorded body bytes replay actually served.
	ProviderBytes int64
	// Elapsed is the body lifetime, from hand-off until EOF, error or Close.
	Elapsed time.Duration
	// Err is the error the replayed body ended with, nil for a clean full body.
	Err error
}

// ReplayStats summarizes one replay session. Results are copied, so callers may
// keep the snapshot after the Replayer is discarded.
type ReplayStats struct {
	// Requests is how many Read calls matched a record.
	Requests int
	// Misses is how many Read calls had no record.
	Misses int
	// Bytes is the total body bytes served to callers across all attempts; a
	// retried range counts the bytes of every attempt, so it can exceed the
	// number of bytes a finished Fetch eventually delivers.
	Bytes int64
	// ProviderBytes is the total recorded body bytes served.
	ProviderBytes int64
	// PeakConcurrent is the highest number of overlapping replayed bodies.
	PeakConcurrent int
	Results        []ReplayResult
}

// Stats returns a snapshot of the replay so far.
func (r *Replayer) Stats() ReplayStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := ReplayStats{
		Requests:       r.requests,
		Misses:         r.misses,
		Bytes:          r.bytes,
		ProviderBytes:  r.providerBytes,
		PeakConcurrent: r.peak,
		Results:        make([]ReplayResult, len(r.results)),
	}
	for i, result := range r.results {
		clone := result
		clone.Chunks = append([]time.Duration(nil), result.Chunks...)
		stats.Results[i] = clone
	}
	return stats
}

// finish records a replayed request once its body is done or its open failed.
func (r *Replayer) finish(result *ReplayResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, *result)
	r.bytes += result.Bytes
	r.providerBytes += result.ProviderBytes
	r.active--
}

// paced converts a recorded nanosecond stamp into real time.
func (r *Replayer) paced(ns int64) time.Duration {
	if ns <= 0 || r.scale == 0 {
		return 0
	}
	return time.Duration(float64(ns) * r.scale)
}

// sleep waits d or returns early with the context error.
func (r *Replayer) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// replayBody serves the recorded chunks of one request at their recorded pace.
type replayBody struct {
	mu       sync.Mutex
	ctx      context.Context
	replayer *Replayer
	record   *RequestRecord
	result   *ReplayResult
	started  time.Time
	chunk    int
	pending  []byte
	done     bool
	err      error
	finished bool
}

func (b *replayBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return 0, b.err
	}
	for len(b.pending) == 0 {
		if b.chunk >= len(b.record.Chunks) {
			b.done = true
			b.err = b.terminalError()
			b.finish()
			return 0, b.err
		}
		chunk := b.record.Chunks[b.chunk]
		if err := b.replayer.sleep(b.ctx, b.replayer.paced(chunk.AtNS)-time.Since(b.started)); err != nil {
			b.err = err
			b.result.Err = err
			b.finish()
			return 0, err
		}
		b.pending = chunk.Data
		b.result.Chunks = append(b.result.Chunks, time.Since(b.started))
		b.result.ProviderBytes += int64(len(b.pending))
		b.chunk++
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	b.result.Bytes += int64(n)
	return n, nil
}

func (b *replayBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.finished {
		return nil
	}
	if err := b.ctx.Err(); err != nil {
		b.err = err
		b.result.Err = err
	} else {
		b.result.Err = ErrIncompleteRecord
	}
	b.finish()
	return nil
}

func (b *replayBody) terminalError() error {
	if b.record.BodyError != "" {
		return replayError(b.record.BodyErrorKind, 0, b.record.BodyError)
	}
	if b.record.Incomplete {
		b.result.Err = ErrIncompleteRecord
		return fmt.Errorf("%w: entry %q offset %d size %d attempt %d",
			ErrIncompleteRecord, b.record.EntryID, b.record.Offset, b.record.Size, b.record.Attempt)
	}
	return io.EOF
}

func (b *replayBody) finish() {
	if b.finished {
		return
	}
	b.done = true
	b.finished = true
	b.result.Elapsed = time.Since(b.started)
	b.replayer.finish(b.result)
}

// compile-time check that Replayer is a Driver.
var _ drive.Driver = (*Replayer)(nil)
