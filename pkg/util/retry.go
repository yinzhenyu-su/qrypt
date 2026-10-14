// Package retry centralizes small retry wait helpers used by drivers.
package util

import (
	"context"
	"time"
)

const DefaultBaseDelay = time.Second

func LinearDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	return time.Duration(attempt+1) * DefaultBaseDelay
}

func Wait(ctx context.Context, attempt int) error {
	timer := time.NewTimer(LinearDelay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func WaitExponential(ctx context.Context, attempt int) error {
	timer := time.NewTimer(ExponentialBackoff(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ExponentialBackoff(attempt int) time.Duration {
	return ExponentialBackoffWithOptions(attempt, 500*time.Millisecond, 0, true)
}

func ExponentialBackoffWithOptions(attempt int, base, maxDelay time.Duration, jitter bool) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if base <= 0 {
		base = DefaultBaseDelay
	}
	// Saturate before multiplication: clamping the returned value cannot
	// repair an overflowed duration (which may already be negative or zero).
	limit := time.Duration(1<<63 - 1)
	if maxDelay > 0 {
		limit = maxDelay
	}
	delay := min(base, limit)
	for i := 0; i < attempt; i++ {
		if delay > limit/2 {
			delay = limit
			break
		}
		delay *= 2
	}
	if jitter {
		// Reduce before multiplying so even the attempt counter cannot
		// overflow. Clamp in floating point before converting to Duration.
		factor := float64(75+(attempt%50)*7%50) / 100.0
		jittered := float64(delay) * factor
		if jittered >= float64(limit) {
			delay = limit
		} else {
			delay = time.Duration(jittered)
		}
	}
	return delay
}
