// Package driverutil provides shared building blocks used by multiple cloud
// drivers. The retry and rate-limit helpers live here so backoff, Retry-After
// handling, and per-mount request budgets have one implementation.
package driverutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// DefaultRetryAttempts is the number of attempts a policy makes when
// MaxAttempts is unset. The first attempt does not count as a retry.
const DefaultRetryAttempts = 3

// DefaultRetryAfterLimit bounds a provider's Retry-After value so a
// misconfigured or hostile response cannot stall a transfer indefinitely.
const DefaultRetryAfterLimit = time.Minute

// RetryPolicy describes how a driver retries one logical request. The zero
// value makes three attempts with exponential backoff.
type RetryPolicy struct {
	// MaxAttempts includes the first attempt. Values below 1 use
	// DefaultRetryAttempts.
	MaxAttempts int
	// MaxRetryAfter bounds a provider Retry-After delay. Zero uses
	// DefaultRetryAfterLimit.
	MaxRetryAfter time.Duration
	// Wait overrides the delay before a retry. attempt is zero-based. A
	// non-nil error aborts the retry loop and is returned to the caller.
	// Tests use it to collapse backoff; production leaves it nil.
	Wait func(ctx context.Context, attempt int, delay time.Duration) error
}

func (p RetryPolicy) attempts() int {
	if p.MaxAttempts > 0 {
		return p.MaxAttempts
	}
	return DefaultRetryAttempts
}

func (p RetryPolicy) retryAfterLimit() time.Duration {
	if p.MaxRetryAfter > 0 {
		return p.MaxRetryAfter
	}
	return DefaultRetryAfterLimit
}

// Do runs attempt until it returns a non-retryable response, a non-retryable
// error, or the policy exhausts its attempts. Retryable responses are closed
// before the next attempt, so callers only read the final body. Retry-After
// overrides exponential backoff when present.
func (p RetryPolicy) Do(ctx context.Context, attempt func(ctx context.Context, attempt int) (*http.Response, error)) (*http.Response, error) {
	if attempt == nil {
		return nil, errors.New("driverutil: retry attempt is nil")
	}
	maxAttempts := p.attempts()
	var retryAfter time.Duration
	var lastErr error
	for index := 0; index < maxAttempts; index++ {
		if index > 0 {
			if err := p.sleep(ctx, index-1, retryAfter); err != nil {
				return nil, err
			}
			retryAfter = 0
		}
		resp, err := attempt(ctx, index)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil || index == maxAttempts-1 || !RetryableTransportError(err) {
				return nil, err
			}
			continue
		}
		if resp == nil {
			lastErr = errors.New("driverutil: retry attempt returned no response")
			if index == maxAttempts-1 {
				return nil, lastErr
			}
			continue
		}
		if index == maxAttempts-1 || !RetryableStatus(resp.StatusCode) {
			return resp, nil
		}
		retryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
		if limit := p.retryAfterLimit(); retryAfter > limit {
			retryAfter = limit
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("driverutil: request failed after %d attempts", maxAttempts)
	}
	return nil, lastErr
}

func (p RetryPolicy) sleep(ctx context.Context, attempt int, delay time.Duration) error {
	if delay <= 0 {
		if p.Wait != nil {
			return p.Wait(ctx, attempt, 0)
		}
		return util.WaitExponential(ctx, attempt)
	}
	if p.Wait != nil {
		return p.Wait(ctx, attempt, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RetryableStatus reports whether a status code is transient. Authentication,
// authorization, missing-resource, conflict, and validation failures are
// deterministic and are never retried.
func RetryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// RetryableTransportError reports whether a transport error is worth another
// attempt. Caller cancellation and deadlines always stop the loop.
func RetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if drive.IsNonRetryable(err) {
		return false
	}
	return true
}

// ParseRetryAfter parses a Retry-After header value, either delay-seconds or
// an HTTP date, and caps it at DefaultRetryAfterLimit. Invalid or past values
// return zero so the caller falls back to normal backoff.
func ParseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		delay := time.Duration(seconds) * time.Second
		if delay > DefaultRetryAfterLimit {
			delay = DefaultRetryAfterLimit
		}
		return delay
	}
	if when, err := http.ParseTime(raw); err == nil {
		delay := time.Until(when)
		if delay <= 0 {
			return 0
		}
		if delay > DefaultRetryAfterLimit {
			delay = DefaultRetryAfterLimit
		}
		return delay
	}
	return 0
}
