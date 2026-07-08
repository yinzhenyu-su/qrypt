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
