package read

import (
	"context"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fakeReadRuntime struct {
	cacheKey      string
	hot           []byte
	rangeData     []byte
	rangeChunk    []byte
	windowData    []byte
	loadData      []byte
	promote       bool
	hotHits       int
	rangeHits     int
	promoteChecks int
	hotPuts       int
	loads         int
	flushes       int
	available     bool
}

func (r *fakeReadRuntime) CacheKey(drive.Entry) string {
	return r.cacheKey
}

func (r *fakeReadRuntime) AddCacheHit() {
	r.hotHits++
}

func (r *fakeReadRuntime) HotChunk(string, int64) ([]byte, bool) {
	return r.hot, r.hot != nil
}

func (r *fakeReadRuntime) PutHotChunk(string, int64, []byte) {
	r.hotPuts++
}

func (r *fakeReadRuntime) ShouldPromoteCachedRange(string, int64) bool {
	r.promoteChecks++
	return r.promote
}

func (r *fakeReadRuntime) RecordCachedRangeHit(string, int64, int64) {
	r.rangeHits++
}

func (r *fakeReadRuntime) FlushStaging(string) error {
	r.flushes++
	return nil
}

func (r *fakeReadRuntime) ChunkAvailable(string, int64) bool {
	return r.available
}

func (r *fakeReadRuntime) GetChunkWithRange(string, int64, int64, int64) ([]byte, []byte, bool, error) {
	return r.rangeData, r.rangeChunk, r.rangeData != nil, nil
}

func (r *fakeReadRuntime) GetChunkRange(string, int64, int64, int64) ([]byte, bool, error) {
	return r.rangeData, r.rangeData != nil, nil
}

func (r *fakeReadRuntime) WaitWindow(context.Context, string, int64) ([]byte, bool, error) {
	return r.windowData, r.windowData != nil, nil
}

func (r *fakeReadRuntime) LoadWindow(context.Context, drive.Entry, int64, int) ([]byte, error) {
	r.loads++
	return r.loadData, nil
}

func (r *fakeReadRuntime) AcquireSlot(context.Context) (func(), error) {
	return func() {}, nil
}

func TestReadChunkRangeWithRuntimeUsesHotChunkFirst(t *testing.T) {
	fs := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	runtime := &fakeReadRuntime{cacheKey: "cache", hot: []byte("abcdef"), loadData: []byte("loaded")}
	data, err := fs.readChunkRangeWithRuntime(context.Background(), drive.Entry{ID: "id", Size: 6, ModTime: time.Now()}, 0, 2, 3, 1, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "cde" {
		t.Fatalf("data = %q, want cde", data)
	}
	if runtime.hotHits != 1 || runtime.loads != 0 {
		t.Fatalf("runtime hotHits=%d loads=%d, want hot hit without load", runtime.hotHits, runtime.loads)
	}
}

func TestReadChunkRangeWithRuntimePromotesRange(t *testing.T) {
	fs := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	runtime := &fakeReadRuntime{cacheKey: "cache", promote: true, rangeData: []byte("abc"), rangeChunk: []byte("abcdef")}
	data, err := fs.readChunkRangeWithRuntime(context.Background(), drive.Entry{ID: "id", Size: 6, ModTime: time.Now()}, 0, 0, 3, 1, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abc" {
		t.Fatalf("data = %q, want abc", data)
	}
	if runtime.promoteChecks != 1 || runtime.hotPuts != 1 || runtime.rangeHits != 0 || runtime.loads != 0 {
		t.Fatalf("runtime promoteChecks=%d hotPuts=%d rangeHits=%d loads=%d", runtime.promoteChecks, runtime.hotPuts, runtime.rangeHits, runtime.loads)
	}
}

func TestReadChunkRangeWithRuntimeFallsBackToLoadWindow(t *testing.T) {
	fs := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	runtime := &fakeReadRuntime{cacheKey: "cache", loadData: []byte("loaded")}
	data, err := fs.readChunkRangeWithRuntime(context.Background(), drive.Entry{ID: "id", Size: 6, ModTime: time.Now()}, 0, 1, 4, 1, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "oade" {
		t.Fatalf("data = %q, want oade", data)
	}
	if runtime.loads != 1 {
		t.Fatalf("loads = %d, want 1", runtime.loads)
	}
}

func TestStateReportsChunkAvailableFromHotChunkAndWindow(t *testing.T) {
	reader := NewReader(ReaderDeps{Host: stubHost{}, State: NewState(nil)})
	runtime := reader.newRuntime()

	reader.putHotChunk("cache", 1, []byte("hot"))
	if !runtime.ChunkAvailable("cache", 1) {
		t.Fatal("hot chunk should be available")
	}

	load := &windowLoad{
		fid:   "cache",
		start: 2,
		end:   4,
		done:  make(chan struct{}),
	}
	reader.state.beginWindowLoad("cache:2:4", load)
	if !runtime.ChunkAvailable("cache", 3) {
		t.Fatal("in-flight window chunk should be available")
	}
	if runtime.ChunkAvailable("cache", 5) {
		t.Fatal("uncovered chunk should not be available")
	}
}

func TestStateOwnsPrefetchReservationState(t *testing.T) {
	state := NewState(nil)

	if !state.reservePrefetch("cache:1:2") {
		t.Fatal("first prefetch reservation should win")
	}
	if state.reservePrefetch("cache:1:2") {
		t.Fatal("duplicate prefetch reservation should be rejected")
	}
	state.releasePrefetch("cache:1:2")
	if !state.reservePrefetch("cache:1:2") {
		t.Fatal("released prefetch reservation should be reusable")
	}
	state.releasePrefetch("cache:1:2")
}

func TestStateConfirmsSequentialReadOnlyAfterCrossingChunkBoundary(t *testing.T) {
	state := NewState(nil)
	if state.observeSequentialRead("cache", 0, ChunkSize) {
		t.Fatal("first chunk should not confirm sequential access")
	}
	if !state.observeSequentialRead("cache", ChunkSize, 16) {
		t.Fatal("contiguous read into the next chunk should confirm sequential access")
	}
	if !state.observeSequentialRead("cache", ChunkSize+16, 16) {
		t.Fatal("contiguous read should retain sequential access")
	}
	if state.observeSequentialRead("cache", 128, 16) {
		t.Fatal("offset jump should reset sequential access")
	}
}

func TestStateTracksSequentialReadsPerSession(t *testing.T) {
	state := NewState(nil)
	first := AccessHint{SessionID: 1, RequestID: 1}
	second := AccessHint{SessionID: 2, RequestID: 1}
	if state.observeReadAccess("cache", first, 0, ChunkSize).sequential {
		t.Fatal("first session confirmed too early")
	}
	if state.observeReadAccess("cache", second, 4*ChunkSize, ChunkSize).sequential {
		t.Fatal("second session confirmed too early")
	}
	first.RequestID++
	if !state.observeReadAccess("cache", first, ChunkSize, 16).sequential {
		t.Fatal("first session did not confirm its own contiguous read")
	}
	second.RequestID++
	if !state.observeReadAccess("cache", second, 5*ChunkSize, 16).sequential {
		t.Fatal("second session was disturbed by the first session")
	}
}

func TestStateTreatsConcurrentAndStaleSessionReadsConservatively(t *testing.T) {
	state := NewState(nil)
	state.observeReadAccess("cache", AccessHint{SessionID: 1, RequestID: 1}, 0, ChunkSize)
	decision := state.observeReadAccess("cache", AccessHint{SessionID: 1, RequestID: 2, Concurrent: true}, ChunkSize, 16)
	if decision.sequential || decision.stale {
		t.Fatalf("concurrent decision = %+v, want current non-sequential", decision)
	}
	decision = state.observeReadAccess("cache", AccessHint{SessionID: 1, RequestID: 1}, 0, ChunkSize)
	if !decision.stale {
		t.Fatal("late request should not replace newer access state")
	}
	if state.readAccessCurrent("cache", AccessHint{SessionID: 1, RequestID: 1}) {
		t.Fatal("older request should not remain current")
	}
	if !state.readAccessCurrent("cache", AccessHint{SessionID: 1, RequestID: 2}) {
		t.Fatal("latest request should remain current")
	}
}

func TestStateReleaseReadSessionClearsItsHistory(t *testing.T) {
	state := NewState(nil)
	hint := AccessHint{SessionID: 7, RequestID: 1}
	state.observeReadAccess("cache", hint, 0, ChunkSize)
	hint.RequestID++
	if !state.observeReadAccess("cache", hint, ChunkSize, 16).sequential {
		t.Fatal("test setup did not confirm sequential access")
	}
	state.ReleaseReadSession(hint.SessionID)
	hint.RequestID++
	if state.observeReadAccess("cache", hint, ChunkSize+16, 16).sequential {
		t.Fatal("released session retained sequential history")
	}
}

func TestClearReadCacheClearsInMemoryFastPaths(t *testing.T) {
	state := NewState(nil)
	state.putHotChunk("cache", 1, []byte("hot"))
	state.recordCachedRangeHit("cache", 1, ChunkSize)
	state.observeSequentialRead("cache", 0, ChunkSize)
	if !state.observeSequentialRead("cache", ChunkSize, 16) {
		t.Fatal("test setup did not confirm sequential read")
	}

	if err := state.ClearReadCache(); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.hotChunk("cache", 1); ok {
		t.Fatal("hot chunk survived ClearReadCache")
	}
	if count, bytes := state.HotChunkStats(); count != 0 || bytes != 0 {
		t.Fatalf("hot chunk stats = %d/%d, want zero", count, bytes)
	}
	if state.observeSequentialRead("cache", ChunkSize+16, 16) {
		t.Fatal("sequential read state survived ClearReadCache")
	}
}
