package read

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type rangeBackend struct {
	mu        sync.Mutex
	data      []byte
	calls     []rangeCall
	failOff   int64
	unsupport bool
}

type rangeCall struct {
	offset int64
	size   int64
}

func (b *rangeBackend) Read(_ context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	b.mu.Lock()
	b.calls = append(b.calls, rangeCall{offset: offset, size: size})
	b.mu.Unlock()
	if b.failOff >= 0 && offset == b.failOff {
		return nil, errors.New("range failed")
	}
	if b.unsupport && size < int64(len(b.data)) {
		return nil, drive.ErrUnsupported
	}
	end := offset + size
	if end > int64(len(b.data)) {
		end = int64(len(b.data))
	}
	if offset > end {
		offset = end
	}
	return io.NopCloser(bytes.NewReader(b.data[offset:end])), nil
}

func (*rangeBackend) CacheKey(drive.Entry) string { return "cache" }

func (*rangeBackend) StoreChunk(context.Context, string, drive.Entry, int64, []byte) {}

func (b *rangeBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func TestFetchChunkWindowParallelSplitsAndAssembles(t *testing.T) {
	data := bytes.Repeat([]byte("a"), ChunkSize*4)
	backend := &rangeBackend{data: data, failOff: -1}
	entry := drive.Entry{ID: "id", Size: int64(len(data))}

	chunks, extra, err := fetchChunkWindowWithPlan(context.Background(), entry, 0, 3, backend, 2, nil, nil, "key")
	if err != nil {
		t.Fatal(err)
	}
	if got := backend.callCount(); got != 2 {
		t.Fatalf("driver calls = %d, want 2", got)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %d, want 4", len(chunks))
	}
	for index := int64(0); index < 4; index++ {
		if !bytes.Equal(chunks[index], bytes.Repeat([]byte("a"), ChunkSize)) {
			t.Fatalf("chunk %d has wrong contents", index)
		}
	}
	if extra["parallel_streams"] != 2 {
		t.Fatalf("parallel_streams = %v, want 2", extra["parallel_streams"])
	}
}

func TestFetchChunkWindowParallelSingleChunkStaysSingle(t *testing.T) {
	data := bytes.Repeat([]byte("b"), ChunkSize)
	backend := &rangeBackend{data: data, failOff: -1}
	entry := drive.Entry{ID: "id", Size: int64(len(data))}

	if _, _, err := fetchChunkWindowWithPlan(context.Background(), entry, 0, 0, backend, 4, nil, nil, "key"); err != nil {
		t.Fatal(err)
	}
	if got := backend.callCount(); got != 1 {
		t.Fatalf("driver calls = %d, want 1", got)
	}
}

func TestFetchChunkWindowParallelUnsupportedFallsBack(t *testing.T) {
	data := bytes.Repeat([]byte("c"), ChunkSize*4)
	backend := &rangeBackend{data: data, failOff: -1, unsupport: true}
	state := NewState(nil)
	entry := drive.Entry{ID: "id", Size: int64(len(data))}

	chunks, _, err := fetchChunkWindowWithPlan(context.Background(), entry, 0, 3, backend, 2, nil, state, "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 {
		t.Fatalf("chunks = %d, want 4", len(chunks))
	}
	if got := backend.callCount(); got < 2 {
		t.Fatalf("driver calls = %d, want at least the parallel attempts", got)
	}
	full := 0
	for _, call := range backend.calls {
		if call.size == int64(len(data)) {
			full++
		}
	}
	if full != 1 {
		t.Fatalf("full-file fallback calls = %d, want 1", full)
	}
	if !state.isNonParallel("key") {
		t.Fatal("file was not marked non-parallel after ErrUnsupported")
	}
}

func TestFetchChunkWindowParallelPartialFailureReturnsError(t *testing.T) {
	data := bytes.Repeat([]byte("d"), ChunkSize*4)
	backend := &rangeBackend{data: data, failOff: ChunkSize * 2}
	entry := drive.Entry{ID: "id", Size: int64(len(data))}

	chunks, _, err := fetchChunkWindowWithPlan(context.Background(), entry, 0, 3, backend, 2, nil, nil, "key")
	if err == nil {
		t.Fatal("expected an error")
	}
	if chunks != nil {
		t.Fatalf("chunks = %v, want nil on failure", chunks)
	}
}

func TestPerFileParallelLimitBlocksAndCancels(t *testing.T) {
	state := NewState(nil)
	ctx := context.Background()
	release1, err := state.acquireParallel(ctx, "key", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	release2, err := state.acquireParallel(ctx, "key", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()

	blockedCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := state.acquireParallel(blockedCtx, "key", 2)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked acquire error = %v, want context.Canceled", err)
	}
}

func TestWindowShapeKeySeparatesStreamCounts(t *testing.T) {
	if got := WindowShapeKey("base", 1); got != "base" {
		t.Fatalf("single-stream key = %q, want base", got)
	}
	if got := WindowShapeKey("base", 4); got == "base" {
		t.Fatalf("parallel key = %q, want a shape suffix", got)
	}
}

func TestFindWindowFiltersByShape(t *testing.T) {
	state := NewState(nil)
	base := WindowKey("fid", 0, 3)
	single := &windowLoad{fid: "fid", start: 0, end: 3, streams: 1, done: make(chan struct{})}
	parallel := &windowLoad{fid: "fid", start: 0, end: 3, streams: 4, done: make(chan struct{})}
	state.windows.loads[WindowShapeKey(base, 1)] = single
	state.windows.loads[WindowShapeKey(base, 4)] = parallel

	if got := state.findWindow("fid", 1, 1); got != single {
		t.Fatalf("single-shape lookup = %p, want %p", got, single)
	}
	if got := state.findWindow("fid", 1, 4); got != parallel {
		t.Fatalf("parallel-shape lookup = %p, want %p", got, parallel)
	}
	if got := state.findWindow("fid", 1, 0); got == nil {
		t.Fatal("any-shape lookup returned nil")
	}
}

type siblingCancelBackend struct {
	firstEntered  chan struct{}
	firstCanceled chan struct{}
}

func (b *siblingCancelBackend) Read(ctx context.Context, _ drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset == 0 {
		close(b.firstEntered)
		<-ctx.Done()
		close(b.firstCanceled)
		return nil, ctx.Err()
	}
	<-b.firstEntered
	return nil, errors.New("sibling failed")
}

func (*siblingCancelBackend) CacheKey(drive.Entry) string { return "cache" }

func (*siblingCancelBackend) StoreChunk(context.Context, string, drive.Entry, int64, []byte) {}

func TestFetchChunkWindowParallelCancelsSibling(t *testing.T) {
	backend := &siblingCancelBackend{
		firstEntered:  make(chan struct{}),
		firstCanceled: make(chan struct{}),
	}
	state := NewState(nil)
	entry := drive.Entry{ID: "id", Size: ChunkSize * 4}

	_, _, err := fetchChunkWindowWithPlan(context.Background(), entry, 0, 3, backend, 2, nil, state, "key")
	if err == nil {
		t.Fatal("expected the sibling failure")
	}
	select {
	case <-backend.firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("sibling sub-range was not canceled")
	}
	state.parallel.mu.Lock()
	files := len(state.parallel.files)
	state.parallel.mu.Unlock()
	if files != 0 {
		t.Fatalf("per-file limiter entries = %d, want 0", files)
	}
}
