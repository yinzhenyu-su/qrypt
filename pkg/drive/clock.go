package drive

import (
	"context"
	"time"

	testclock "github.com/yinzhenyu/qrypt/pkg/clock"
)

// clockContextKey is the context key for the time seam used by the shared
// fetch layer.
type clockContextKey struct{}

// WithClock installs clk as the clock used by the fetch layer for retry
// backoff and timeout accounting. It is a test seam: production code never
// installs a clock, so the layer keeps using the wall clock through
// testclock.Real.
//
// A nil clock leaves ctx unchanged. Callers that need a deterministic clock
// can bundle it with the fetch timeouts they already pass on the context.
func WithClock(ctx context.Context, clk testclock.Clock) context.Context {
	if clk == nil {
		return ctx
	}
	return context.WithValue(ctx, clockContextKey{}, clk)
}

// clockFrom returns the clock installed by WithClock, or the wall clock.
func clockFrom(ctx context.Context) testclock.Clock {
	if ctx != nil {
		if clk, ok := ctx.Value(clockContextKey{}).(testclock.Clock); ok && clk != nil {
			return clk
		}
	}
	return testclock.Real
}

// wakeAfter is a convenience wrapper for select-based waits.
func wakeAfter(ctx context.Context, d time.Duration) <-chan time.Time {
	return clockFrom(ctx).After(d)
}
