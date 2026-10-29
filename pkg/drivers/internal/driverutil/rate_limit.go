package driverutil

import (
	"context"

	"golang.org/x/time/rate"
)

// RateLimiter bounds a driver's API request rate. One instance belongs to one
// provider client and is shared by every concurrent call that client makes.
// A nil *RateLimiter is valid and never waits, so callers can leave the
// limiter disabled without a branch.
type RateLimiter struct {
	limiter *rate.Limiter
}

// NewRateLimiter returns a limiter that admits perSecond requests per second,
// or nil when perSecond is not positive.
func NewRateLimiter(perSecond int) *RateLimiter {
	if perSecond <= 0 {
		return nil
	}
	return &RateLimiter{limiter: rate.NewLimiter(rate.Limit(perSecond), 1)}
}

// Wait blocks until the limiter admits one request or ctx ends.
func (l *RateLimiter) Wait(ctx context.Context) error {
	if l == nil || l.limiter == nil {
		return nil
	}
	return l.limiter.Wait(ctx)
}
