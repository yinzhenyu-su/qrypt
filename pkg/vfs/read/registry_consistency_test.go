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

// registryBlockingHost holds every prefetch in flight until its context is
// canceled, so the test can inspect both cancellation registries while the
// prefetches are live.
type registryBlockingHost struct {
	entry    drive.Entry
	entered  chan struct{}
	canceled chan struct{}
	mu       sync.Mutex
	started  int
	stopped  int
}

func newRegistryBlockingHost() *registryBlockingHost {
	return &registryBlockingHost{
		entry:    drive.Entry{ID: "id", Size: 4 * ChunkSize, ModTime: time.Unix(1700000000, 0)},
		entered:  make(chan struct{}, 4),
		canceled: make(chan struct{}, 4),
	}
}

func (h *registryBlockingHost) Resolve(context.Context, string) (drive.Entry, error) {
	return h.entry, nil
}

func (*registryBlockingHost) PendingUpload(string) (vfstypes.PendingUpload, bool, error) {
	return vfstypes.PendingUpload{}, false, nil
}

func (*registryBlockingHost) ReadCacheKey(drive.Entry) string { return "cache" }

func (h *registryBlockingHost) DriverRead(ctx context.Context, _ drive.Entry, _, _ int64) (io.ReadCloser, error) {
	h.mu.Lock()
	h.started++
	h.mu.Unlock()
	h.entered <- struct{}{}
	<-ctx.Done()
	h.mu.Lock()
	h.stopped++
	h.mu.Unlock()
	h.canceled <- struct{}{}
	return nil, ctx.Err()
}

func (h *registryBlockingHost) counts() (started, stopped int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started, h.stopped
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPrefetchRegistriesDrainTogether(t *testing.T) {
	cases := []struct {
		name   string
		cancel func(*State)
	}{
		{"invalidate", func(s *State) { s.InvalidateFile("cache") }},
		{"session-release", func(s *State) { s.ReleaseReadSession(7) }},
		{"legacy-seek", func(s *State) { s.cancelPrefetch("cache") }},
		{"close", func(s *State) { _ = s.Close() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := newRegistryBlockingHost()
			state := NewState(nil)
			reader := NewReader(ReaderDeps{Host: host, State: state})
			ctx := WithAccessHint(context.Background(), AccessHint{SessionID: 7, RequestID: 1})

			reader.prefetchWindow(ctx, host.entry, 1, 1)
			reader.prefetchWindow(ctx, host.entry, 2, 1)
			for i := 0; i < 2; i++ {
				select {
				case <-host.entered:
				case <-time.After(time.Second):
					t.Fatal("prefetch did not enter the driver")
				}
			}

			state.prefetch.mu.Lock()
			legacy := len(state.prefetch.cancels["cache"])
			state.prefetch.mu.Unlock()
			state.scopes.mu.Lock()
			scopes := len(state.scopes.byCache["cache"])
			state.scopes.mu.Unlock()
			if legacy != 2 {
				t.Fatalf("legacy prefetch registry entries = %d, want 2", legacy)
			}
			if scopes != 1 {
				t.Fatalf("scope registry entries = %d, want 1 shared scope", scopes)
			}

			tc.cancel(state)
			for i := 0; i < 2; i++ {
				select {
				case <-host.canceled:
				case <-time.After(time.Second):
					t.Fatal("prefetch driver read was not canceled")
				}
			}
			waitUntil(t, "both registries to drain", func() bool {
				state.prefetch.mu.Lock()
				legacy := len(state.prefetch.cancels["cache"])
				state.prefetch.mu.Unlock()
				state.scopes.mu.Lock()
				scopes := len(state.scopes.byCache["cache"])
				state.scopes.mu.Unlock()
				return legacy == 0 && scopes == 0
			})
			if started, stopped := host.counts(); started != 2 || stopped != 2 {
				t.Fatalf("driver reads started=%d stopped=%d, want 2/2", started, stopped)
			}
		})
	}
}
