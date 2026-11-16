package drive

import (
	"context"
	"errors"
	"io"
	"net/url"
	"testing"
	"testing/synctest"
	"time"
)

type providerTimeoutReader struct{ err error }

func (r providerTimeoutReader) Read([]byte) (int, error) { return 0, r.err }

func TestProviderDeadlineDoesNotImpersonateFetchDeadline(t *testing.T) {
	for _, stage := range []string{"open", "body"} {
		for _, progressive := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "/buffered", true: "/progressive"}[progressive], func(t *testing.T) {
				// net/http response-header timeouts are net.Errors that also
				// satisfy errors.Is(DeadlineExceeded), while our budget is live.
				providerErr := &url.Error{Op: "Get", URL: "https://example.invalid", Err: context.DeadlineExceeded}
				open := func(context.Context, Entry, int64, int64) (io.ReadCloser, error) {
					if stage == "open" {
						return nil, providerErr
					}
					return io.NopCloser(providerTimeoutReader{providerErr}), nil
				}
				var delivery *rangeDelivery
				if progressive {
					delivery = &rangeDelivery{emit: func(int64, []byte) { t.Error("published failed body") }}
				}
				ctx := context.Background()
				_, _, err := readWindowRangeAttempt(ctx, Entry{ID: "file", Size: 1}, 0, 1, time.Second, time.Minute, open, delivery)
				if !errors.Is(err, providerErr) || IsFetchTimeout(err) {
					t.Fatalf("provider timeout lost its identity: %v", err)
				}
				trace := &ReadTrace{}
				trace.failure(ctx, err)
				if got := trace.Snapshot()["failure_reason"]; got != string(ReadNetworkFailure) {
					t.Fatalf("failure reason = %v, want network failure", got)
				}
			})
		}
	}
}

func TestFetchOpenDeadlineAndCallerCancellationRemainDistinct(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		open := func(ctx context.Context, _ Entry, _, _ int64) (io.ReadCloser, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		_, _, err := readWindowRangeAttempt(context.Background(), Entry{ID: "file", Size: 1}, 0, 1, time.Second, 10*time.Millisecond, open, nil)
		var timeout *FetchTimeoutError
		if !errors.As(err, &timeout) || timeout.Kind != FetchTimeoutTotal {
			t.Fatalf("expired own budget = %v, want fetch total", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = readWindowRangeAttempt(ctx, Entry{ID: "file", Size: 1}, 0, 1, time.Second, time.Minute, open, nil)
		if !errors.Is(err, context.Canceled) || IsFetchTimeout(err) {
			t.Fatalf("caller cancellation = %v", err)
		}
	})
}
