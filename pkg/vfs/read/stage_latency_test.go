package read

// Deterministic read staging harness.
//
// The seek-policy tests need to talk about *order*, not wall-clock luck: "the
// seek target was delivered before the lookahead finished", "session A's seek
// did not disturb session B". stagedHost makes that explicit by letting a test
// gate individual Range requests on a channel. A gated Range blocks inside
// DriverRead until the test releases it or the read context is canceled, so no
// test has to sleep to create a race.
//
// Every Range is recorded in issue order with its offset, size, and start
// time. Compatibility tests assert the current window and prefetch behaviour;
// the stage-policy tests added later assert the new behaviour on the same
// harness.

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type stagedCall struct {
	Offset int64
	Size   int64
	At     time.Time
	// Plan is the read plan carried into this Range.
	Plan ReadPlan
	// Err is the error the Range ended with, nil when it delivered bytes.
	Err error
	// Prefetch marks a speculative Range.
	Prefetch bool
	// Gated is true when the call waited on the test's release channel.
	Gated bool
}

func (c stagedCall) end() int64 { return c.Offset + c.Size }

type stagedHost struct {
	entry    drive.Entry
	data     []byte
	cacheKey string

	mu     sync.Mutex
	calls  []stagedCall
	gateFn func(offset, size int64) (release <-chan struct{}, ok bool)
	// holdCancel keeps a gated call blocked until its release channel closes
	// even when ctx ends. Provider bodies do not always notice a canceled
	// request promptly; tests use it to keep an abandoned fetch in flight while
	// the foreground moves on.
	holdCancel bool
	active     int
	maxActive  int
	// plan is the last read plan carried into a driver Range, so tests can
	// assert the resolved stage without reaching into the reader.
	plan ReadPlan
}

func (h *stagedHost) Resolve(context.Context, string) (drive.Entry, error) { return h.entry, nil }

func (*stagedHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (h *stagedHost) ReadCacheKey(drive.Entry) string { return h.cacheKey }

func (h *stagedHost) DriverRead(ctx context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	h.mu.Lock()
	call := stagedCall{Offset: offset, Size: size, At: time.Now()}
	var release <-chan struct{}
	if h.gateFn != nil {
		if ch, ok := h.gateFn(offset, size); ok {
			release = ch
			call.Gated = true
		}
	}
	plan, _ := readPlanFromContext(ctx)
	call.Plan = plan
	call.Prefetch = isPrefetchRead(ctx)
	h.plan = plan
	h.calls = append(h.calls, call)
	index := len(h.calls) - 1
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()

	done := func() {
		h.mu.Lock()
		h.active--
		h.mu.Unlock()
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			if !h.holdCancel {
				h.markCallError(index, ctx.Err())
				done()
				return nil, ctx.Err()
			}
			<-release
		}
	}

	start, end := offset, offset+size
	if start > int64(len(h.data)) {
		start = int64(len(h.data))
	}
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	if start > end {
		start = end
	}
	return &stagedBody{ReadCloser: io.NopCloser(bytes.NewReader(h.data[start:end])), done: done}, nil
}

type stagedBody struct {
	io.ReadCloser
	done func()
	once sync.Once
}

func (b *stagedBody) Close() error {
	b.once.Do(b.done)
	return b.ReadCloser.Close()
}

func (h *stagedHost) markCallError(index int, err error) {
	h.mu.Lock()
	if index >= 0 && index < len(h.calls) {
		h.calls[index].Err = err
	}
	h.mu.Unlock()
}

func (h *stagedHost) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// maxConcurrent reports the highest number of overlapping Range requests seen.
func (h *stagedHost) maxConcurrent() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxActive
}

func (h *stagedHost) lastPlan() ReadPlan {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.plan
}

func (h *stagedHost) recorded() []stagedCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]stagedCall(nil), h.calls...)
}

// ranges returns every Range whose end lies inside [start, end).
func (h *stagedHost) rangesWithin(start, end int64) []stagedCall {
	var out []stagedCall
	for _, call := range h.recorded() {
		if call.Offset >= start && call.end() <= end {
			out = append(out, call)
		}
	}
	return out
}

// rangesOverlapping returns every Range that covers any byte of [start, end).
// Prefetch runs asynchronously, so an assertion must select the Ranges that
// actually served the requested span instead of slicing by call count.
func (h *stagedHost) rangesOverlapping(start, end int64) []stagedCall {
	var out []stagedCall
	for _, call := range h.recorded() {
		if call.Offset < end && call.end() > start {
			out = append(out, call)
		}
	}
	return out
}

// waitRangeSize blocks until a Range of exactly size bytes has been issued.
// Prefetch runs in a goroutine, so the lookahead Range is not observable
// synchronously after the foreground read returns.
func (h *stagedHost) waitRangeSize(t *testing.T, size int64) stagedCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, call := range h.recorded() {
			if call.Size == size {
				return call
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %d-byte lookahead Range issued: %+v", size, h.recorded())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitCall blocks until at least n Range requests have been issued.
func (h *stagedHost) waitCall(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.callCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("driver ranges = %d, want at least %d", h.callCount(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

type stagedReader struct {
	reader *Reader
	state  *State
	host   *stagedHost
}

func stagedContent(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	return data
}

func newStagedReader(t *testing.T, size int) *stagedReader {
	t.Helper()
	return newStagedReaderWithProfile(t, size, drive.DefaultReadPolicy())
}

func newStagedReaderWithProfile(t *testing.T, size int, profile drive.ReadPolicy) *stagedReader {
	t.Helper()
	host := &stagedHost{
		entry:    drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: int64(size), ModTime: time.Unix(1700000000, 0)},
		data:     stagedContent(size),
		cacheKey: "cache",
	}
	state := NewState(nil)
	reader := newTestReader(testReaderDeps{Host: host, State: state, Profile: profile})
	t.Cleanup(func() { _ = state.Close() })
	return &stagedReader{reader: reader, state: state, host: host}
}

// read performs one mount-style read for a session. session/request identify
// the open-file stream the way FUSE's access hint does, so sequential detection,
// prefetch scoping, and seek cancellation behave as they do on a mounted file.
func (s *stagedReader) read(t *testing.T, ctx context.Context, offset, size int64) []byte {
	t.Helper()
	rc, err := s.reader.Read(ctx, "/file.bin", offset, size)
	if err != nil {
		t.Fatalf("Read(%d,%d): %v", offset, size, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(%d,%d): %v", offset, size, err)
	}
	return data
}

func sessionContext(session, request uint64) context.Context {
	return WithAccessHint(context.Background(), AccessHint{SessionID: session, RequestID: request})
}

func TestStageSmallSeekMergesCrossChunkWindow(t *testing.T) {
	size := 8 << 20
	r := newStagedReader(t, size)
	// A player seek commonly lands mid-chunk. The read must cover the two
	// touched chunks in one Range and return exactly the requested bytes.
	offset := int64(ChunkSize) + 1000
	got := r.read(t, sessionContext(1, 2), offset, 64<<10)

	if !bytes.Equal(got, r.host.data[offset:offset+64<<10]) {
		t.Fatal("seek returned wrong bytes")
	}
	calls := r.host.recorded()
	if len(calls) != 1 {
		t.Fatalf("ranges = %d (%+v), want exactly one merged window", len(calls), calls)
	}
	if calls[0].Offset != ChunkSize || calls[0].Size != 2*ChunkSize {
		t.Fatalf("window = offset %d size %d, want aligned 2 chunks", calls[0].Offset, calls[0].Size)
	}
}

func TestStageUnalignedWindowNeverReadsPastRequest(t *testing.T) {
	size := 8 << 20
	r := newStagedReader(t, size)
	offset := int64(2*ChunkSize) + 4096
	got := r.read(t, sessionContext(1, 1), offset, 100<<10)
	if !bytes.Equal(got, r.host.data[offset:offset+100<<10]) {
		t.Fatal("unaligned read returned wrong bytes")
	}
	for _, call := range r.host.recorded() {
		if call.Size > 2*ChunkSize {
			t.Fatalf("unaligned seek window = %d bytes, want at most two chunks", call.Size)
		}
	}
}

func TestStageSequentialReadPrefetchesOneLargeWindow(t *testing.T) {
	size := 64 << 20
	r := newStagedReader(t, size)
	// An adaptive pass starts from a small seed and widens only as the caller
	// consumes bytes. Reaching the stage's 8 MiB window and 32 MiB horizon
	// takes 1/2/4/8/16 MiB of successive consumption (§ design 2), so this
	// continuous pass reads until a wide window must appear: the ramp length,
	// not a policy constant, decides how many reads it needs. A recorded call
	// stays visible once issued, so a short poll per read catches the wide
	// window; the final waitRangeSize is the fallback if the ramp outruns it.
	want := int64(SequentialPrefetchChunks) * ChunkSize
	for chunk := int64(0); chunk < 128; chunk++ {
		r.read(t, sessionContext(1, uint64(chunk+1)), chunk*ChunkSize, ChunkSize)
		for deadline := time.Now().Add(2 * time.Millisecond); ; {
			for _, call := range r.host.recorded() {
				if call.Prefetch && call.Size == want {
					return
				}
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	r.host.waitRangeSize(t, want)
}

func TestStagePrefetchDisabledIssuesOnlyTheTarget(t *testing.T) {
	r := newStagedReader(t, 8<<20)
	ctx := WithoutReadPrefetch(sessionContext(1, 1))
	r.read(t, ctx, 0, ChunkSize)
	if got := r.host.callCount(); got != 1 {
		t.Fatalf("ranges = %d, want only the target read", got)
	}
}

func TestStageCacheHitSkipsProvider(t *testing.T) {
	r := newStagedReader(t, 8<<20)
	r.state.putHotChunk("cache", 0, append([]byte(nil), r.host.data[:ChunkSize]...))
	got := r.read(t, sessionContext(1, 1), 0, 16<<10)
	if !bytes.Equal(got, r.host.data[:16<<10]) {
		t.Fatal("cache hit returned wrong bytes")
	}
	// Speculative lookahead may run concurrently; only the requested span
	// proves whether the cache hit avoided the provider.
	if calls := r.host.rangesWithin(0, ChunkSize); len(calls) != 0 {
		t.Fatalf("driver ranges in the cached span = %d, want 0", len(calls))
	}
}

func TestStageMultiSessionDoesNotShareSpeculativeWait(t *testing.T) {
	r := newStagedReader(t, 64<<20)
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	defer closeRelease()
	// Gate only session A's lookahead chunk so its prefetch is still in flight
	// while session B reads elsewhere.
	const gatedOffset = ChunkSize
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == gatedOffset {
			return release, true
		}
		return nil, false
	}

	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitCall(t, 2) // target + gated lookahead

	// Session B's foreground read must not wait on session A's speculative
	// Range. Read in a goroutine so a regression surfaces as a timeout, not a
	// hung test binary.
	done := make(chan []byte, 1)
	go func() {
		done <- r.read(t, sessionContext(2, 1), 32<<20, ChunkSize)
	}()
	select {
	case got := <-done:
		if !bytes.Equal(got, r.host.data[32<<20:32<<20+ChunkSize]) {
			t.Fatal("session B returned wrong bytes")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session B foreground read waited on session A's speculative Range")
	}
}

func TestStageSessionReleaseCancelsOnlyItsPrefetch(t *testing.T) {
	r := newStagedReader(t, 64<<20)
	release := make(chan struct{})
	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	defer closeRelease()
	r.host.gateFn = func(offset, size int64) (<-chan struct{}, bool) {
		if offset == ChunkSize {
			return release, true
		}
		return nil, false
	}

	r.read(t, sessionContext(1, 1), 0, ChunkSize)
	r.host.waitCall(t, 2)

	// A seek in the same session drops the obsolete lookahead. The gate stays
	// closed, so only cancellation can release the blocked Range.
	r.state.cancelScopesBySession(1, "seek")

	deadline := time.Now().Add(2 * time.Second)
	for {
		r.host.mu.Lock()
		active := r.host.active
		r.host.mu.Unlock()
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gated prefetch still active (%d) after session cancel", active)
		}
		time.Sleep(time.Millisecond)
	}
}
