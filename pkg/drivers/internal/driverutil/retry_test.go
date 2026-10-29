package driverutil_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
)

type responseSpec struct {
	status int
	header http.Header
	err    error
}

func fakeResponse(status int, retryAfter string) *http.Response {
	header := http.Header{}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("body")),
	}
}

func TestRetryPolicyHonorsRetryAfter(t *testing.T) {
	specs := []responseSpec{
		{status: http.StatusTooManyRequests},
		{status: http.StatusOK},
	}
	specs[0].header = http.Header{"Retry-After": []string{"2"}}
	var delays []time.Duration
	policy := driverutil.RetryPolicy{
		MaxAttempts: 2,
		Wait: func(_ context.Context, _ int, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	attempts := 0
	resp, err := policy.Do(context.Background(), func(context.Context, int) (*http.Response, error) {
		spec := specs[attempts]
		attempts++
		header := spec.header
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{StatusCode: spec.status, Header: header, Body: io.NopCloser(strings.NewReader("x"))}, spec.err
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("attempts = %d, status = %d", attempts, resp.StatusCode)
	}
	if len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("delays = %v, want [2s]", delays)
	}
}

func TestRetryPolicyDoesNotRetryDeterministicStatuses(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusUnprocessableEntity,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			policy := driverutil.RetryPolicy{MaxAttempts: 3}
			resp, err := policy.Do(context.Background(), func(context.Context, int) (*http.Response, error) {
				attempts++
				return fakeResponse(status, ""), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || resp.StatusCode != status {
				t.Fatalf("attempts = %d, status = %d", attempts, resp.StatusCode)
			}
		})
	}
}

func TestRetryPolicyRetriesTransportErrorsAndStopsOnCancel(t *testing.T) {
	transport := errors.New("connection reset")
	attempts := 0
	policy := driverutil.RetryPolicy{MaxAttempts: 3, Wait: noWait}
	resp, err := policy.Do(context.Background(), func(context.Context, int) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, transport
		}
		return fakeResponse(http.StatusOK, ""), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("attempts = %d, status = %d", attempts, resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts = 0
	_, err = policy.Do(ctx, func(context.Context, int) (*http.Response, error) {
		attempts++
		return nil, transport
	})
	if !errors.Is(err, transport) {
		t.Fatalf("cancelled retry error = %v, want transport error", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts after cancel = %d, want 1", attempts)
	}
}

func TestRetryPolicyExhaustsBoundedAttempts(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	policy := driverutil.RetryPolicy{
		MaxAttempts: 3,
		Wait: func(_ context.Context, _ int, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	}
	resp, err := policy.Do(context.Background(), func(context.Context, int) (*http.Response, error) {
		attempts++
		return fakeResponse(http.StatusBadGateway, ""), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("attempts = %d, status = %d", attempts, resp.StatusCode)
	}
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two backoff steps", delays)
	}
}

func TestRetryPolicyWaitErrorStopsRetries(t *testing.T) {
	stop := errors.New("stop")
	policy := driverutil.RetryPolicy{
		MaxAttempts: 3,
		Wait: func(context.Context, int, time.Duration) error {
			return stop
		},
	}
	_, err := policy.Do(context.Background(), func(context.Context, int) (*http.Response, error) {
		return fakeResponse(http.StatusInternalServerError, ""), nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("error = %v, want wait error", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := driverutil.ParseRetryAfter("2"); got != 2*time.Second {
		t.Fatalf("seconds = %v", got)
	}
	if got := driverutil.ParseRetryAfter("0"); got != 0 {
		t.Fatalf("zero = %v", got)
	}
	if got := driverutil.ParseRetryAfter("-1"); got != 0 {
		t.Fatalf("negative = %v", got)
	}
	if got := driverutil.ParseRetryAfter("garbage"); got != 0 {
		t.Fatalf("garbage = %v", got)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if got := driverutil.ParseRetryAfter(future); got <= 0 || got > 3*time.Second {
		t.Fatalf("date = %v", got)
	}
	if got := driverutil.ParseRetryAfter("999999"); got != time.Minute {
		t.Fatalf("cap = %v, want one minute", got)
	}
}

func TestRateLimiterIsSharedAndCancellable(t *testing.T) {
	var nilLimiter *driverutil.RateLimiter
	if err := nilLimiter.Wait(context.Background()); err != nil {
		t.Fatalf("nil limiter wait: %v", err)
	}
	if driverutil.NewRateLimiter(0) != nil {
		t.Fatal("zero rate should disable the limiter")
	}
	limiter := driverutil.NewRateLimiter(1000)
	if limiter == nil {
		t.Fatal("positive rate did not create a limiter")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- limiter.Wait(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent wait: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := driverutil.NewRateLimiter(1).Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}

func noWait(context.Context, int, time.Duration) error { return nil }
