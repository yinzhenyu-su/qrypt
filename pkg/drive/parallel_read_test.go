package drive

import (
	"bytes"
	"context"
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

func TestReadParallelSplitsLargeFile(t *testing.T) {
	data := bytes.Repeat([]byte("a"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
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

func TestReadParallelSmallFileUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("b"), 1<<20)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
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

func TestReadParallelInternalParallelDriverStaysSingle(t *testing.T) {
	data := bytes.Repeat([]byte("c"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange, CapabilityInternalParallelRead)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
	}
}

func TestReadParallelUnknownSizeUsesSingleRead(t *testing.T) {
	data := bytes.Repeat([]byte("d"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin"}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if got := driver.reads.Load(); got != 1 {
		t.Fatalf("driver reads = %d, want 1", got)
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

func TestReadParallelFallsBackOnStrictUnsupported(t *testing.T) {
	data := bytes.Repeat([]byte("e"), directReadPartSize*2)
	fake := NewFakeDriver(FakeWithCapabilities(CapabilityParallelRange))
	if err := fake.Seed(map[string]string{"/file.bin": string(data)}); err != nil {
		t.Fatal(err)
	}
	driver := &strictFallbackReadDriver{FakeDriver: fake}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallel(context.Background(), driver, entry, 0, 0)
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
	if got := driver.fullReads.Load(); got != 1 {
		t.Fatalf("full-stream reads = %d, want 1", got)
	}
	if driver.strictCalls.Load() == 0 {
		t.Fatal("strict range preflight was not used")
	}
}

func TestDirectReadUsesSharedBudget(t *testing.T) {
	if got := directReadScheduler.Stats().NormalCapacity; got != directReadSlots {
		t.Fatalf("direct read capacity = %d, want %d", got, directReadSlots)
	}
}

func TestReadParallelWithProfileUsesExplicitProfile(t *testing.T) {
	data := bytes.Repeat([]byte("e"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}

	rc, err := ReadParallelWithProfile(context.Background(), driver, entry, 0, 0, ReadProfile{BulkStreams: 2})
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
	profile := ReadProfile{
		BulkStreams:       1,
		FetchIdleTimeout:  50 * time.Millisecond,
		FetchTotalTimeout: 5 * time.Second,
	}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := ReadParallelWithProfile(context.Background(), driver, entry, 0, 0, profile)
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
	data := bytes.Repeat([]byte("f"), directReadPartSize*2)
	driver := seededDriver(t, data, CapabilityParallelRange)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, _ []byte) io.ReadCloser {
		return &stalledParallelBody{ctx: ctx}
	}
	profile := ReadProfile{
		BulkStreams:       2,
		ParallelThreshold: 1,
		FetchIdleTimeout:  50 * time.Millisecond,
		FetchTotalTimeout: 5 * time.Second,
	}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := ReadParallelWithProfile(context.Background(), driver, entry, 0, 0, profile)
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
	profile := ReadProfile{
		BulkStreams:       1,
		FetchIdleTimeout:  100 * time.Millisecond,
		FetchTotalTimeout: 5 * time.Second,
	}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := ReadParallelWithProfile(context.Background(), driver, entry, 0, 0, profile)
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
	data := bytes.Repeat([]byte("h"), directReadPartSize+1024)
	driver := seededDriver(t, data)
	driver.ReadBehavior.BodyFunc = func(ctx context.Context, span []byte) io.ReadCloser {
		return &pacedParallelBody{ctx: ctx, remaining: len(span), chunk: 8 << 10, interval: 2 * time.Millisecond}
	}
	// The total bound (1.2s) is shorter than the whole-file stream (~2s), so a
	// regression that applies it to a stream fails here. The idle bound stays
	// generous enough for a loaded CI/race runner.
	profile := ReadProfile{
		BulkStreams:       1,
		FetchIdleTimeout:  time.Second,
		FetchTotalTimeout: 1200 * time.Millisecond,
	}
	entry := Entry{ID: "0/file.bin", Size: int64(len(data))}
	rc, err := ReadParallelWithProfile(context.Background(), driver, entry, 0, 0, profile)
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
