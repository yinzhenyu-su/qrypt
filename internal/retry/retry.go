// Package retry centralizes small retry wait helpers used by drivers.
package retry

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
	delay := base
	for i := 0; i < attempt; i++ {
		if maxDelay > 0 && delay >= maxDelay/2 {
			delay = maxDelay
			break
		}
		delay *= 2
	}
	if maxDelay > 0 && delay > maxDelay {
		delay = maxDelay
	}
	if jitter {
		factor := float64(75+(attempt*7)%50) / 100.0
		delay = time.Duration(float64(delay) * factor)
		if maxDelay > 0 && delay > maxDelay {
			delay = maxDelay
		}
	}
	return delay
}
