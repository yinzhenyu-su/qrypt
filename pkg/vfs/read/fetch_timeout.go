package read

import (
	"context"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type fetchTimeoutsContextKey struct{}

// WithFetchTimeouts attaches resolved read-fetch bounds to ctx. The reader
// attaches them from its driver profile so every path (foreground, prefetch,
// stream) uses the same model.
func WithFetchTimeouts(ctx context.Context, timeouts drive.FetchTimeouts) context.Context {
	return context.WithValue(ctx, fetchTimeoutsContextKey{}, timeouts)
}

// fetchTimeoutsFromContext returns the attached bounds, or conservative
// defaults when a caller bypassed the reader entry points (unit tests, direct
// window fetches).
func fetchTimeoutsFromContext(ctx context.Context) drive.FetchTimeouts {
	if timeouts, ok := ctx.Value(fetchTimeoutsContextKey{}).(drive.FetchTimeouts); ok &&
		timeouts.Idle > 0 && timeouts.Total > 0 {
		return timeouts
	}
	return drive.DefaultReadProfile().FetchTimeouts()
}

// withFetchTimeouts ensures ctx carries this reader's driver-derived bounds.
func (r *Reader) withFetchTimeouts(ctx context.Context) context.Context {
	if _, ok := ctx.Value(fetchTimeoutsContextKey{}).(drive.FetchTimeouts); ok {
		return ctx
	}
	return WithFetchTimeouts(ctx, r.profile.FetchTimeouts())
}

// TimeoutWindow describes the most recent read fetch timeout.
type TimeoutWindow struct {
	CacheKey   string
	Start      int64
	End        int64
	Background bool
	Kind       drive.FetchTimeoutKind
	At         time.Time
}

// fetchTiming accumulates the wait and fetch time of one read call so its
// summary event can report both.
type fetchTiming struct {
	mu    sync.Mutex
	wait  time.Duration
	fetch time.Duration
}

type fetchTimingContextKey struct{}

// withFetchTiming attaches a per-call timing accumulator to ctx.
func withFetchTiming(ctx context.Context) (context.Context, *fetchTiming) {
	timing := &fetchTiming{}
	return context.WithValue(ctx, fetchTimingContextKey{}, timing), timing
}

func fetchTimingFromContext(ctx context.Context) *fetchTiming {
	timing, _ := ctx.Value(fetchTimingContextKey{}).(*fetchTiming)
	return timing
}

// ensureFetchTiming re-attaches timing to a context supplied by a caller that
// does not carry it (ReadContext during a stream refill).
func ensureFetchTiming(ctx context.Context, timing *fetchTiming) context.Context {
	if timing == nil || fetchTimingFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, fetchTimingContextKey{}, timing)
}

func (t *fetchTiming) recordWait(d time.Duration) {
	if t == nil || d <= 0 {
		return
	}
	t.mu.Lock()
	t.wait += d
	t.mu.Unlock()
}

func (t *fetchTiming) recordFetch(d time.Duration) {
	if t == nil || d <= 0 {
		return
	}
	t.mu.Lock()
	t.fetch += d
	t.mu.Unlock()
}

func (t *fetchTiming) attach(extra map[string]any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	wait, fetch := t.wait, t.fetch
	t.mu.Unlock()
	extra["wait_us"] = wait.Microseconds()
	extra["fetch_us"] = fetch.Microseconds()
}

// timeoutStats counts fetch timeouts and remembers the most recent window.
// Foreground and prefetch timeouts stay separate: they have different failure
// semantics and operators need to tell them apart.
type timeoutStats struct {
	mu         sync.Mutex
	foreground int
	prefetch   int
	last       *TimeoutWindow
}

func (s *State) recordFetchTimeouts(background bool, cacheKey string, start, end int64, kind drive.FetchTimeoutKind, count int) {
	if s == nil || count <= 0 {
		return
	}
	if kind == "" {
		kind = drive.FetchTimeoutIdle
	}
	s.timeouts.mu.Lock()
	defer s.timeouts.mu.Unlock()
	if background {
		s.timeouts.prefetch += count
	} else {
		s.timeouts.foreground += count
	}
	s.timeouts.last = &TimeoutWindow{
		CacheKey:   cacheKey,
		Start:      start,
		End:        end,
		Background: background,
		Kind:       kind,
		At:         time.Now(),
	}
}

// recordWindowFetchTimeouts records the timeouts one window fetch reported in
// its diagnostics extra. It runs on both the success and failure paths so a
// retried timeout is still counted.
func recordWindowFetchTimeouts(state *State, background bool, cacheKey string, start, end int64, extra map[string]any) {
	count, _ := extra["fetch_timeouts"].(int)
	if count <= 0 {
		return
	}
	kind := drive.FetchTimeoutIdle
	if value, ok := extra["fetch_timeout_kind"].(string); ok && value != "" {
		kind = drive.FetchTimeoutKind(value)
	}
	state.recordFetchTimeouts(background, cacheKey, start, end, kind, count)
}

// TimeoutStats reports foreground and prefetch fetch-timeout counts plus the
// most recent timeout window.
func (s *State) TimeoutStats() (foreground, prefetch int, last *TimeoutWindow) {
	if s == nil {
		return 0, 0, nil
	}
	s.timeouts.mu.Lock()
	defer s.timeouts.mu.Unlock()
	if s.timeouts.last == nil {
		return s.timeouts.foreground, s.timeouts.prefetch, nil
	}
	lastCopy := *s.timeouts.last
	return s.timeouts.foreground, s.timeouts.prefetch, &lastCopy
}
