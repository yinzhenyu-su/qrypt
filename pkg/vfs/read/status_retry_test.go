package read

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// scriptedReadBackend fails its first len(failures) Read calls in order, then
// serves zero bytes. A nil entry means "this call succeeds".
type scriptedReadBackend struct {
	failures []error
	calls    int
}

func (b *scriptedReadBackend) Read(_ context.Context, _ drive.Entry, _ int64, size int64) (io.ReadCloser, error) {
	b.calls++
	if len(b.failures) > 0 {
		err := b.failures[0]
		b.failures = b.failures[1:]
		if err != nil {
			return nil, err
		}
	}
	return io.NopCloser(bytes.NewReader(make([]byte, size))), nil
}

func (b *scriptedReadBackend) CacheKey(drive.Entry) string { return "" }

func (b *scriptedReadBackend) StoreChunk(context.Context, string, drive.Entry, int64, []byte) {}

// providerStatus builds the error a driver produces for a status that has no
// dedicated sentinel, through the same constructor drivers use.
func providerStatus(t *testing.T, code int) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://provider.invalid/content/api/v2.1/files/get?path=%2Ffile.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Request: req}
	return drive.HTTPError("koofr: read", req, resp, []byte(`{"error":{"code":"Other"}}`))
}

func rangeRetryContext(t *testing.T) context.Context {
	t.Helper()
	return WithFetchTimeouts(context.Background(), drive.FetchTimeouts{Idle: time.Second, Total: 5 * time.Second})
}

func rangeEntry() drive.Entry {
	return drive.Entry{ID: "0/file.bin", Name: "file.bin", Size: ChunkSize}
}

// TestReadWindowRangeReplaysTransientStatus: one 5xx on a Range GET is replayed
// after a backoff instead of failing the read, and the replay is visible in the
// window extras.
func TestReadWindowRangeReplaysTransientStatus(t *testing.T) {
	backend := &scriptedReadBackend{failures: []error{providerStatus(t, http.StatusInternalServerError)}}
	started := time.Now()
	data, extra, err := readWindowRange(rangeRetryContext(t), rangeEntry(), 0, 0, backend)
	if err != nil {
		t.Fatalf("read failed despite a transient 500: %v", err)
	}
	if len(data) != ChunkSize {
		t.Fatalf("bytes = %d, want %d", len(data), ChunkSize)
	}
	if backend.calls != 2 {
		t.Fatalf("driver calls = %d, want the failed attempt plus one replay", backend.calls)
	}
	if got := extra["fetch_status_retries"]; got != 1 {
		t.Fatalf("fetch_status_retries = %v, want 1", got)
	}
	if got := extra["fetch_status_reason"]; got != "status_5xx" {
		t.Fatalf("fetch_status_reason = %v, want status_5xx", got)
	}
	if got := extra["fetch_attempts"]; got != 2 {
		t.Fatalf("fetch_attempts = %v, want 2", got)
	}
	if got := extra["fetch_retried"]; got != true {
		t.Fatalf("fetch_retried = %v, want true", got)
	}
	// The replay must wait, not hammer the provider that just failed.
	if elapsed := time.Since(started); elapsed < fetchStatusBackoffBase*3/4 {
		t.Fatalf("replayed after %s, want at least ~%s", elapsed, fetchStatusBackoffBase)
	}
}

// TestReadWindowRangeStopsAfterStatusBudget: a provider that fails every request
// keeps the fast failure path - bounded attempts, the provider error (not a
// timeout we caused), and no unbounded retry loop.
func TestReadWindowRangeStopsAfterStatusBudget(t *testing.T) {
	status := providerStatus(t, http.StatusInternalServerError)
	backend := &scriptedReadBackend{failures: []error{status, status, status, status, status}}
	_, extra, err := readWindowRange(rangeRetryContext(t), rangeEntry(), 0, 0, backend)
	if err == nil {
		t.Fatal("read succeeded despite a persistent 500")
	}
	if drive.IsFetchTimeout(err) {
		t.Fatalf("error = %v, want the provider error, not a fetch timeout", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want it to carry the provider status", err)
	}
	if backend.calls != fetchStatusAttempts {
		t.Fatalf("driver calls = %d, want %d", backend.calls, fetchStatusAttempts)
	}
	if got := extra["fetch_status_retries"]; got != fetchStatusAttempts-1 {
		t.Fatalf("fetch_status_retries = %v, want %d", got, fetchStatusAttempts-1)
	}
}

// TestReadWindowRangeReplaysRateLimit: 429 reaches the read path as
// drive.ErrRateLimit and is replayed with the rate-limit reason.
func TestReadWindowRangeReplaysRateLimit(t *testing.T) {
	backend := &scriptedReadBackend{failures: []error{
		fmt.Errorf("%w: 429 Too Many Requests", drive.ErrRateLimit),
	}}
	if _, extra, err := readWindowRange(rangeRetryContext(t), rangeEntry(), 0, 0, backend); err != nil {
		t.Fatalf("read failed despite a transient 429: %v", err)
	} else if got := extra["fetch_status_reason"]; got != "rate_limited" {
		t.Fatalf("fetch_status_reason = %v, want rate_limited", got)
	}
	if backend.calls != 2 {
		t.Fatalf("driver calls = %d, want the failed attempt plus one replay", backend.calls)
	}
}

// TestReadWindowRangeDoesNotReplayPermanentFailures: classified failures keep
// their single-attempt fast path.
func TestReadWindowRangeDoesNotReplayPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "not found", err: providerStatus(t, http.StatusNotFound)},
		{name: "unauthorized", err: providerStatus(t, http.StatusUnauthorized)},
		{name: "unsupported range", err: drive.ErrUnsupported},
		{name: "plain error", err: errors.New("provider said no")},
		// A driver may know a status is not worth replaying (for example a
		// provider-state 5xx) and say so explicitly.
		{name: "non-retryable 5xx", err: drive.NonRetryable(providerStatus(t, http.StatusInternalServerError))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &scriptedReadBackend{failures: []error{tc.err}}
			if _, _, err := readWindowRange(rangeRetryContext(t), rangeEntry(), 0, 0, backend); err == nil {
				t.Fatal("read succeeded, want the provider failure")
			}
			if backend.calls != 1 {
				t.Fatalf("driver calls = %d, want 1 (no replay for %s)", backend.calls, tc.name)
			}
		})
	}
}

// TestReadWindowRangeStatusBackoffHonorsContext: the backoff never outlives the
// caller's context, and the caller's cancellation is what surfaces.
func TestReadWindowRangeStatusBackoffHonorsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(rangeRetryContext(t), 50*time.Millisecond)
	defer cancel()
	backend := &scriptedReadBackend{failures: []error{providerStatus(t, http.StatusInternalServerError)}}
	started := time.Now()
	_, _, err := readWindowRange(ctx, rangeEntry(), 0, 0, backend)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the caller's deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("read returned after %s, want it bounded by the caller's context", elapsed)
	}
	if backend.calls != 1 {
		t.Fatalf("driver calls = %d, want 1", backend.calls)
	}
}

// TestFetchStatusBackoffDoublesAndCaps pins the backoff sequence so a future
// change cannot silently turn the retry into a hot loop or a long stall.
func TestFetchStatusBackoffDoublesAndCaps(t *testing.T) {
	want := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second, 2 * time.Second}
	for i, expected := range want {
		if got := fetchStatusBackoff(i + 1); got != expected {
			t.Fatalf("fetchStatusBackoff(%d) = %s, want %s", i+1, got, expected)
		}
	}
}
