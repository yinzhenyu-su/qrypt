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

type countingReadDriver struct {
	*FakeDriver
	reads atomic.Int32
}

func (d *countingReadDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	d.reads.Add(1)
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func seededDriver(t *testing.T, data []byte, caps ...Capability) *countingReadDriver {
	t.Helper()
	fake := NewFakeDriver(FakeWithCapabilities(caps...))
	if err := fake.Seed(map[string]string{"/file.bin": string(data)}); err != nil {
		t.Fatal(err)
	}
	return &countingReadDriver{FakeDriver: fake}
}

func TestSourceReadSplitsLargeFile(t *testing.T) {
	data := bytes.Repeat([]byte("a"), (8<<20)*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := readSourceDefault(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("parallel read returned different bytes")
	}
	if got := driver.reads.Load(); got < 2 {
		t.Fatalf("driver reads = %d, want at least 2", got)
	}
}

// TestReadDirectPartReplaysTruncatedBody: a part body that ends before the
// declared range must be replayed once instead of being copied out short.
func TestReadDirectPartReplaysTruncatedBody(t *testing.T) {
	data := bytes.Repeat([]byte("t"), 1<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	var attempts atomic.Int32
	driver.ReadBehavior = ReadBehavior{BodyFunc: func(_ context.Context, span []byte) io.ReadCloser {
		if attempts.Add(1) == 1 {
			return io.NopCloser(bytes.NewReader(span[:len(span)/2]))
		}
		return io.NopCloser(bytes.NewReader(span))
	}}

	got, err := fetchSourcePart(context.Background(), driver, entry, 0, int64(len(data)), DefaultReadPolicy().Fetch)
	if err != nil {
		t.Fatalf("readDirectPart: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("part data differs: got=%d want=%d", len(got), len(data))
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want the truncated attempt plus one replay", got)
	}
}

// TestReadDirectPartFailsAfterTruncatedBodyBudget: a provider that always ends
// the body early fails the part instead of retrying forever.
func TestReadDirectPartFailsAfterTruncatedBodyBudget(t *testing.T) {
	data := bytes.Repeat([]byte("u"), 1<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	var attempts atomic.Int32
	driver.ReadBehavior = ReadBehavior{BodyFunc: func(_ context.Context, span []byte) io.ReadCloser {
		attempts.Add(1)
		return io.NopCloser(bytes.NewReader(span[:len(span)/2]))
	}}

	if _, err := fetchSourcePart(context.Background(), driver, entry, 0, int64(len(data)), DefaultReadPolicy().Fetch); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want %d", got, 2)
	}
}

func TestSourceReadSmallFileUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("b"), 1<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := readSourceDefault(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("single read returned different bytes")
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
	}
}

func TestSourceReadInternalParallelDriverStaysSingle(t *testing.T) {
	data := bytes.Repeat([]byte("c"), (8<<20)*2)
	driver := seededDriver(t, data, CapabilityParallelRange, CapabilityInternalParallelRead)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := readSourceDefault(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 2 {
		t.Fatalf("driver reads = %d, want 2", got)
	}
}

func TestSourceReadUnknownSizeUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("d"), (8<<20)*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin"}

	rc, err := readSourceDefault(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 3 {
		t.Fatalf("driver reads = %d, want 3", got)
	}
}

type strictFallbackReadDriver struct {
	*FakeDriver
	strictCalls atomic.Int32
	fullReads   atomic.Int32
}

func (d *strictFallbackReadDriver) Capabilities() []Capability {
	return []Capability{CapabilityParallelRange}
}

func (d *strictFallbackReadDriver) Read(ctx context.Context, entry Entry, offset, size int64) (io.ReadCloser, error) {
	if StrictRangeFromContext(ctx) {
		d.strictCalls.Add(1)
		return nil, ErrUnsupported
	}
	d.fullReads.Add(1)
	return d.FakeDriver.Read(ctx, entry, offset, size)
}

func TestSourceReadFallsBackOnStrictUnsupported(t *testing.T) {
	data := bytes.Repeat([]byte("e"), (8<<20)*2)
	fake := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange))
	if err := fake.Seed(map[string]string{"/file.bin": string(data)}); err != nil {
		t.Fatal(err)
	}
	driver := &strictFallbackReadDriver{FakeDriver: fake}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := readSourceDefault(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("fallback read returned different bytes")
	}
	if got := driver.fullReads.Load(); got != 2 {
		t.Fatalf("single-stream windows = %d, want 2", got)
	}
	if driver.strictCalls.Load() == 0 {
		t.Fatal("strict range preflight was not used")
	}
}

func TestSourceReadWithProfileUsesExplicitProfile(t *testing.T) {
	data := bytes.Repeat([]byte("e"), (8<<20)*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, func() ReadPolicy { p := DefaultReadPolicy(); p.Bulk.Streams = 2; return p }())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got < 3 {
		t.Fatalf("driver reads = %d, want a strict probe plus two parts", got)
	}
}

type stalledParallelBody struct{ ctx context.Context }

func (b *stalledParallelBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *stalledParallelBody) Close() error { return nil }

type pacedParallelBody struct {
	ctx       context.Context
	remaining int
	chunk     int
	interval  time.Duration
}

func (b *pacedParallelBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	interval := b.interval
	if interval <= 0 {
		interval = 5 * time.Millisecond
	}
	select {
	case <-time.After(interval):
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	chunk := b.chunk
	if chunk <= 0 {
		chunk = 1024
	}
	n := min(len(p), chunk, b.remaining)
	for i := 0; i < n; i++ {
		p[i] = 'd'
	}
	b.remaining -= n
	return n, nil
}
func (b *pacedParallelBody) Close() error { return nil }

func TestDirectReadSingleStreamTimesOutStalledBody(t *testing.T) {
	data := bytes.Repeat([]byte("e"), 1<<20)
	driver := seededDriver(t, data)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, _ []byte) io.ReadCloser {
		return &stalledParallelBody{ctx: ctx}
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 50 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	started := time.Now()
	_, err = io.ReadAll(rc)
	if !IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("direct read returned after %s, want it bounded", elapsed)
	}
}

func TestDirectReadParallelPartsTimeOutStalledBody(t *testing.T) {
	data := bytes.Repeat([]byte("f"), (8<<20)*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, _ []byte) io.ReadCloser {
		return &stalledParallelBody{ctx: ctx}
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 2
		p.Bulk.Split.MinParallelBytes = 1
		p.Fetch.Idle = 50 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	_, err = io.ReadAll(rc)
	if !IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
}

func TestDirectReadAllowsSlowProgress(t *testing.T) {
	data := bytes.Repeat([]byte("g"), 8<<10)
	driver := seededDriver(t, data)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, span []byte) io.ReadCloser {
		return &pacedParallelBody{ctx: ctx, remaining: len(span)}
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 100 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("bytes = %d, want %d", len(got), len(data))
	}
}

func TestDirectReadWholeFileStreamIsNotCappedByFetchTotal(t *testing.T) {
	// 2 MiB + 1 KiB at 8 KiB per 2 ms paces the stream at ~510ms. The file is
	// still delivered whole and still spans more than one fetch window; the
	// size is only large enough for the stream to outrun the total bound.
	data := bytes.Repeat([]byte("h"), (2<<20)+1024)
	driver := seededDriver(t, data)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, span []byte) io.ReadCloser {
		return &pacedParallelBody{ctx: ctx, remaining: len(span), chunk: 8 << 10, interval: 2 * time.Millisecond}
	}
	// The stream refills window by window (128 KiB at this ~4 MiB/s pacing is
	// ~32 ms each), far below the 400 ms total, so a loaded runner cannot trip
	// the bound on one refill. The whole file (~510 ms) still outruns the
	// total, so a regression that applies it to the whole-file stream fails.
	// The idle bound (300 ms) is 150x the 2 ms pacing gap.
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Bulk.WindowBytes = 128 << 10
		p.Fetch.Idle = 300 * time.Millisecond
		p.Fetch.Total = 400 * time.Millisecond
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("whole-file stream failed despite steady progress: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("bytes = %d, want %d", len(got), len(data))
	}
}

// waitForReadActive blocks until the fake driver reports at least one in-flight
// read, so a concurrency assertion does not race the part goroutines.
func waitForReadActive(t *testing.T, driver *FakeDriver, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for driver.ReadStats().Active < want {
		if time.Now().After(deadline) {
			t.Fatalf("in-flight reads = %d, want at least %d", driver.ReadStats().Active, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// The direct path must use the same read profile shape as the VFS read domain:
// the fixed part size and the per-file in-flight budget apply to fs get and
// share fetch too. A budget of one with 512 KiB parts used to issue three
// concurrent 8 MiB requests.
func TestSourceReadHonorsPartSizeAndInFlightBudget(t *testing.T) {
	const total = 24 << 20
	data := bytes.Repeat([]byte("q"), total)
	driver := seededDriver(t, data, CapabilityParallelRange)
	release := make(chan struct{})
	driver.ReadBehavior.GateFunc = func(_, size int64) <-chan struct{} {
		if size == 1 {
			// The capability probe must not be gated behind the parts.
			return nil
		}
		return release
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 3
		p.Bulk.Split.MinParallelBytes = 1
		p.MaxInFlight = 1
		p.Bulk.Split.PartBytes = int64(1) * ReadChunkSize
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: total}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() { data, err := io.ReadAll(rc); done <- readResult{data, err} }()
	waitForReadActive(t, driver.FakeDriver, 1)
	time.Sleep(100 * time.Millisecond)
	if stats := driver.ReadStats(); stats.Active != 1 || stats.MaxConcurrent != 1 {
		t.Fatalf("in-flight reads = %d (max %d), want the one-permit budget", stats.Active, stats.MaxConcurrent)
	}
	for _, call := range driver.ReadCalls() {
		if call.Size != 1 && call.Size != ReadChunkSize {
			t.Fatalf("part = offset %d size %d, want %d-byte parts", call.Offset, call.Size, ReadChunkSize)
		}
	}
	close(release)
	result := <-done
	got, err := result.data, result.err
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("direct read returned different bytes")
	}
	wantParts := int(total / ReadChunkSize)
	if calls := driver.ReadCalls(); len(calls) != wantParts {
		t.Fatalf("reads = %d, want %d bounded parts", len(calls), wantParts)
	}
}

// A request below the profile's split threshold stays one Range even when the
// stream count would allow splitting it.
func TestSourceReadMinSplitKeepsSmallRequestSingleStream(t *testing.T) {
	data := bytes.Repeat([]byte("r"), 4<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 3
		p.Bulk.Split.MinParallelBytes = 1
		p.Bulk.Split.MinParallelBytes = int64(16) * ReadChunkSize
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("direct read returned different bytes")
	}
	if calls := driver.ReadStats().Calls; calls != 1 {
		t.Fatalf("reads = %d, want one below the split threshold", calls)
	}
}

// The direct read path (`fs get`, share fetch) must ignore local bandwidth
// waits for the same reason as the mounted path: the provider is not stalling.
func TestDirectReadIgnoresLocalBandwidthWait(t *testing.T) {
	data := bytes.Repeat([]byte("w"), 64<<10)
	driver := seededDriver(t, data)
	limiter := NewBandwidthLimiter(BandwidthLimits{DownloadBytesPerSecond: 512 << 10})
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, span []byte) io.ReadCloser {
		return limiter.LimitDownload(ctx, io.NopCloser(bytes.NewReader(span)))
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 40 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	// 64 KiB at 512 KiB/s needs about 125 ms, past the idle bound.
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("local rate-limit wait reported as a stall: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("direct read returned different bytes")
	}
}

// causeTestBody blocks until its request context ends, then reports the cancel
// cause the driver would observe.
type causeTestBody struct {
	ctx      context.Context
	cause    chan<- error
	progress time.Duration
}

func (b *causeTestBody) Read(p []byte) (int, error) {
	if b.progress > 0 {
		timer := time.NewTimer(b.progress)
		defer timer.Stop()
		select {
		case <-timer.C:
			p[0] = 1
			return 1, nil
		case <-b.ctx.Done():
		}
	} else {
		<-b.ctx.Done()
	}
	select {
	case b.cause <- context.Cause(b.ctx):
	default:
	}
	return 0, b.ctx.Err()
}

func (b *causeTestBody) Close() error { return nil }

// stalledDirectDriver returns a driver whose bodies stall, leaving the fetch to
// the profile's bounds.
func stalledDirectDriver(t *testing.T, size int, causes chan<- error) *countingReadDriver {
	t.Helper()
	driver := seededDriver(t, bytes.Repeat([]byte("s"), size), CapabilityParallelRange)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, span []byte) io.ReadCloser {
		if len(span) == 1 {
			// The capability probe answers normally.
			return io.NopCloser(bytes.NewReader(span))
		}
		return &causeTestBody{ctx: ctx, cause: causes}
	}
	return driver
}

func readCauses(t *testing.T, causes <-chan error, want int) []error {
	t.Helper()
	var got []error
	deadline := time.After(2 * time.Second)
	for len(got) < want {
		select {
		case cause := <-causes:
			got = append(got, cause)
		case <-deadline:
			t.Fatalf("driver observed %d cancellations, want %d", len(got), want)
		}
	}
	return got
}

// An abandoned direct read must end its request with ReadIdleStall, the same
// way the VFS window path does: a driver rotates away from a stalled
// connection only when it can tell that case from a caller cancel.
func TestDirectReadAbandonPropagatesStallCause(t *testing.T) {
	causes := make(chan error, 8)
	entry := Entry{ID: "0/file.bin", Size: 1 << 20}
	driver := stalledDirectDriver(t, 1<<20, causes)
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 50 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if !IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	if cause := readCauses(t, causes, 1)[0]; !errors.Is(cause, ReadIdleStall) {
		t.Fatalf("cancel cause = %v, want ReadIdleStall", cause)
	}
}

// The total bound is an abandoned fetch too, not a caller cancel.
func TestDirectReadTotalTimeoutPropagatesStallCause(t *testing.T) {
	causes := make(chan error, 8)
	entry := Entry{ID: "0/file.bin", Size: 1 << 20}
	driver := stalledDirectDriver(t, 1<<20, causes)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, _ []byte) io.ReadCloser {
		return &causeTestBody{ctx: ctx, cause: causes, progress: 10 * time.Millisecond}
	}
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 100 * time.Millisecond
		p.Fetch.Total = 100 * time.Millisecond
		return p
	}()
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if !IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	if cause := readCauses(t, causes, 1)[0]; !errors.Is(cause, ReadTotalDeadline) {
		t.Fatalf("cancel cause = %v, want ReadTotalDeadline", cause)
	}
}

// A caller cancel must stay a caller cancel: marking it as a stall would make
// drivers rotate connections for aborted downloads.
func TestDirectReadCallerCancelKeepsPlainCause(t *testing.T) {
	causes := make(chan error, 8)
	entry := Entry{ID: "0/file.bin", Size: 1 << 20}
	driver := stalledDirectDriver(t, 1<<20, causes)
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 1
		p.Fetch.Idle = 5 * time.Second
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := readSourceWithPolicy(ctx, driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if err == nil {
		t.Fatal("read succeeded after the caller canceled")
	}
	cause := readCauses(t, causes, 1)[0]
	if errors.Is(cause, ReadIdleStall) {
		t.Fatalf("caller cancel reported as a stall: %v", cause)
	}
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("cancel cause = %v, want context.Canceled", cause)
	}
}

// The chunked path abandons each stalled part with the same cause.
func TestDirectPartAbandonPropagatesStallCause(t *testing.T) {
	causes := make(chan error, 8)
	entry := Entry{ID: "0/file.bin", Size: 16 << 20}
	driver := stalledDirectDriver(t, 16<<20, causes)
	profile := func() ReadPolicy {
		p := DefaultReadPolicy()
		p.Bulk.Streams = 2
		p.Bulk.Split.MinParallelBytes = 1
		p.Fetch.Idle = 50 * time.Millisecond
		p.Fetch.Total = 5 * time.Second
		return p
	}()
	rc, err := readSourceWithPolicy(context.Background(), driver, entry, 0, 0, profile)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = io.ReadAll(rc)
	_ = rc.Close()
	if !IsFetchTimeout(err) {
		t.Fatalf("read error = %v, want a fetch timeout", err)
	}
	for _, cause := range readCauses(t, causes, 1) {
		if !errors.Is(cause, ReadIdleStall) {
			t.Fatalf("part cancel cause = %v, want ReadIdleStall", cause)
		}
	}
}

// Two concurrent direct reads of the same file share one per-file in-flight
// budget. Capping only each reader's workers lets two readers use the whole
// budget twice, so a budget of one still puts two Ranges in flight.

// A single-stream body is one in-flight Range, so it holds the file permit
// until the caller closes it and a second reader waits instead of overlapping.

type configuredSourceDriver struct {
	Driver
	policy ReadPolicy
}

func (d configuredSourceDriver) ReadPolicy() ReadPolicy { return d.policy }
func readSourceWithPolicy(ctx context.Context, d Driver, e Entry, o, n int64, p ReadPolicy) (io.ReadCloser, error) {
	s, err := NewReadSource(configuredSourceDriver{d, p}, nil)
	if err != nil {
		return nil, err
	}
	r, err := s.Read(ctx, e, o, n)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return &ownedSourceBody{ReadCloser: r, source: s}, nil
}

type ownedSourceBody struct {
	io.ReadCloser
	source *ReadSource
}

func (b *ownedSourceBody) Close() error { return errors.Join(b.ReadCloser.Close(), b.source.Close()) }
func readSourceDefault(ctx context.Context, d Driver, e Entry, o, n int64) (io.ReadCloser, error) {
	return readSourceWithPolicy(ctx, d, e, o, n, DefaultReadPolicy())
}
func fetchSourcePart(ctx context.Context, d Driver, e Entry, o, n int64, b FetchTimeouts) ([]byte, error) {
	s, err := NewReadSource(d, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.Fetch(ctx, e, RangePlan{Offset: o, Size: n, Streams: 1, Timeouts: b})
}

func TestFetchStatusBackoffDoublesAndCaps(t *testing.T) {
	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second, 2 * time.Second}
	for i, expected := range want {
		if got := fetchStatusBackoff(i + 1); got != expected {
			t.Fatalf("fetchStatusBackoff(%d) = %s, want %s", i+1, got, expected)
		}
	}
}
