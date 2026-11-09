package read

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/vfs/vfstypes"
)

type blockingHost struct {
	entry    drive.Entry
	entered  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (h *blockingHost) Resolve(context.Context, string) (drive.Entry, error) {
	return h.entry, nil
}

func (*blockingHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (*blockingHost) ReadCacheKey(drive.Entry) string { return "cache" }

func (h *blockingHost) DriverRead(ctx context.Context, _ drive.Entry, _, _ int64) (io.ReadCloser, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	h.once.Do(func() { close(h.canceled) })
	return nil, ctx.Err()
}

func startBlockingPrefetch(t *testing.T, ctx context.Context) (*State, *blockingHost) {
	t.Helper()
	host := &blockingHost{
		entry:    drive.Entry{ID: "id", Size: 4 * ChunkSize, ModTime: time.Now()},
		entered:  make(chan struct{}, 1),
		canceled: make(chan struct{}),
	}
	state := NewState(nil)
	reader := NewReader(ReaderDeps{Host: host, State: state})
	reader.prefetchWindow(ctx, host.entry, 1, 1)

	select {
	case <-host.entered:
	case <-time.After(time.Second):
		t.Fatal("prefetch did not enter the driver read")
	}
	return state, host
}

func waitCanceled(t *testing.T, host *blockingHost) {
	t.Helper()
	select {
	case <-host.canceled:
	case <-time.After(time.Second):
		t.Fatal("prefetch driver read was not canceled")
	}
}

func TestPrefetchStopsOnInvalidate(t *testing.T) {
	state, host := startBlockingPrefetch(t, context.Background())
	state.InvalidateFile("cache")
	waitCanceled(t, host)
}

func TestPrefetchStopsOnSessionRelease(t *testing.T) {
	ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 7, RequestID: 1})
	state, host := startBlockingPrefetch(t, ctx)
	state.ReleaseReadSession(7)
	waitCanceled(t, host)
}

func TestPrefetchStopsOnCallerCancel(t *testing.T) {
	ctx := context.Background()
	state, host := startBlockingPrefetch(t, ctx)
	state.cancelReadScope(ctx, "cache", "caller")
	waitCanceled(t, host)
}

func TestPrefetchStopsOnStateClose(t *testing.T) {
	state, host := startBlockingPrefetch(t, context.Background())
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	waitCanceled(t, host)
}
