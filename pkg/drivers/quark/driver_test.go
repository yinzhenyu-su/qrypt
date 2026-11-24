package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cryptpkg "github.com/yinzhenyu/qrypt/pkg/crypt"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/testsupport/testknobs"
)

func TestResolvePathRootUsesConfiguredRootID(t *testing.T) {
	d := &Driver{rootID: "root-fid"}
	got, err := d.ResolvePath(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "root-fid" {
		t.Fatalf("ResolvePath root = %q, want configured root id", got)
	}
}

func TestDriverInitListAndResolveRootPath(t *testing.T) {
	var seenCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		if r.URL.Path != "/file/sort" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		parent := r.URL.Query().Get("pdir_fid")
		switch parent {
		case "0":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "root-docs", "file_name": "Docs", "file": false, "size": 0},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		case "root-docs":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "file-1", "file_name": "a.txt", "file": true, "file_size": 12, "created_at": 1699990000000, "updated_at": 1700000000000},
					},
				},
				"metadata": map[string]any{"_total": 1},
			})
		default:
			t.Fatalf("unexpected parent: %s", parent)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{RootPath: "/Docs", BaseURL: server.URL, V2URL: server.URL})
	if err := driver.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seenCookie != "k=v" {
		t.Fatalf("cookie header = %q, want k=v", seenCookie)
	}

	entries, err := driver.List(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ID != "file-1" || entry.ParentID != "root-docs" || entry.Name != "a.txt" || entry.IsDir || entry.Size != 12 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() || !entry.ModTime.Equal(entry.UpdatedAt) {
		t.Fatalf("unexpected entry times: %+v", entry)
	}
}

func TestRegisterQuarkDriver(t *testing.T) {
	driver, err := drive.New("quark", drive.Params{
		"cookie":   "k=v",
		"base_url": "http://127.0.0.1",
		"v2_url":   "http://127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*Driver); !ok {
		t.Fatalf("driver type = %T, want *quark.Driver", driver)
	}
}

func TestDriverReadRetriesDownloadWithoutRefreshingURL(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		apiCalls++
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	var downloadCalls, actualCalls int
	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		actualCalls++
		if actualCalls == 1 {
			return nil, &net.DNSError{Name: req.URL.Host, Err: "no such host"}
		}
		if got := req.Header.Get("User-Agent"); got != downloadChromeUA {
			t.Fatalf("download user-agent = %q, want Chrome UA", got)
		}
		if got := req.Header.Get("Referer"); got != "https://pan.quark.cn/" {
			t.Fatalf("download referer = %q, want trailing slash", got)
		}
		if got := req.Header.Get("Accept"); got != "*/*" {
			t.Fatalf("download accept = %q, want */*", got)
		}
		if got := req.Header.Get("Range"); got != "bytes=4-8" {
			t.Fatalf("range = %q, want bytes=4-8", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("hello")),
			Request:    req,
		}, nil
	})

	rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("body = %q, want hello", data)
	}
	if apiCalls != 1 {
		t.Fatalf("download URL calls = %d, want 1", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("download calls = %d, want 2", downloadCalls)
	}
	if _, ok := driver.getURL("fid-1"); !ok {
		t.Fatal("download URL cache was invalidated after transient network error")
	}
	metrics := driver.cl.metricEvents(time.Time{})
	if got := metricCount(metrics, "download_url"); got != 1 {
		t.Fatalf("download_url metric count = %d, want 1", got)
	}
	if got := metricCount(metrics, "download"); got != 2 {
		t.Fatalf("download metric count = %d, want 2", got)
	}
	var sawDownload bool
	for _, event := range metrics {
		if event.Operation != "download" || event.Attempt != 2 {
			continue
		}
		sawDownload = true
		if event.RemoteID != "fid-1" {
			t.Fatalf("download remote_id = %q, want fid-1", event.RemoteID)
		}
		if event.Offset != 4 || event.Requested != 5 {
			t.Fatalf("download offset/size = %d/%d, want 4/5", event.Offset, event.Requested)
		}
		if got := event.Request["range"]; got != "bytes=4-8" {
			t.Fatalf("download range = %v, want bytes=4-8", got)
		}
		if got := event.Request["url_host"]; got != "download.test" {
			t.Fatalf("download url_host = %v, want download.test", got)
		}
		if got := event.Request["url_cache_hit"]; got != false {
			t.Fatalf("download url_cache_hit = %v, want false", got)
		}
	}
	if !sawDownload {
		t.Fatal("missing second download metric")
	}
}

func TestDriverReadClampsAtEOF(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	var downloadCalls int
	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		if got := req.Header.Get("Range"); got != "bytes=8-9" {
			t.Fatalf("range = %q, want bytes=8-9", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("89")),
			Request:    req,
		}, nil
	})

	entry := drive.Entry{ID: "fid-eof", Name: "file.bin", Size: 10}
	for _, offset := range []int64{10, 11} {
		rc, err := driver.Read(context.Background(), entry, offset, 1)
		if err != nil {
			t.Fatalf("read at offset %d: %v", offset, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read at offset %d body: %v", offset, err)
		}
		if len(data) != 0 {
			t.Fatalf("read at offset %d returned %d bytes, want empty", offset, len(data))
		}
	}
	if downloadCalls != 0 {
		t.Fatalf("EOF reads issued %d download calls, want 0", downloadCalls)
	}

	rc, err := driver.Read(context.Background(), entry, 8, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "89" {
		t.Fatalf("clamped read = %q, want 89", data)
	}
	if downloadCalls != 1 {
		t.Fatalf("download calls = %d, want 1", downloadCalls)
	}
}

func TestDriverReadStrictRangeRejectsWholeObject(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("whole object")),
			Request:    req,
		}, nil
	})

	entry := drive.Entry{ID: "fid-strict", Name: "file.bin", Size: 100}
	if _, err := driver.Read(drive.WithStrictRange(context.Background()), entry, 0, 10); !errors.Is(err, drive.ErrUnsupported) {
		t.Fatalf("strict read error = %v, want drive.ErrUnsupported", err)
	}
	rc, err := driver.Read(context.Background(), entry, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "whole object" {
		t.Fatalf("non-strict body = %q, want whole object", data)
	}
}

func TestDriverReadPolicyPreservesLegacyReadShape(t *testing.T) {
	d := New("cookie", Options{})
	p := d.ReadPolicy()
	if !drive.HasCapability(d, drive.CapabilityParallelRange) || drive.HasCapability(d, drive.CapabilityInternalParallelRead) {
		t.Fatal("invalid read capability")
	}
	if p.Bulk.WindowBytes != 4<<20 || p.Bulk.Streams != 3 || p.Bulk.PartBytes != 8<<20 || p.Bulk.MinParallelBytes != 4<<20 {
		t.Fatalf("bulk=%+v", p.Bulk)
	}
	if p.Interactive.WindowBytes != 2<<20 || p.Interactive.PrefetchDepth != 4 {
		t.Fatalf("interactive=%+v", p.Interactive)
	}
	if p.MaxInFlight != 7 {
		t.Fatalf("file budget=%d, want room for four lookaheads plus three workers", p.MaxInFlight)
	}
	if p.Fetch.Total != downloadTotalTimeout || p.Fetch.Idle <= 0 || p.Fetch.Idle > p.Fetch.Total {
		t.Fatalf("fetch=%+v", p.Fetch)
	}
}

// failingReadBody fails on the first read, like a CDN connection reset.
type failingReadBody struct{}

func (failingReadBody) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }
func (failingReadBody) Close() error             { return nil }

// TestDriverRotatesDownloadTransportAfterBodyFailure: a failed body read must
// replace the download connection generation so a later read (or the read
// layer's retry) does not reuse the degraded edge.
func TestDriverRotatesDownloadTransportAfterBodyFailure(t *testing.T) {
	const downloadURL = "https://download.test/file.bin"
	driver := New("k=v", Options{})
	driver.setURL("fid-1", downloadURL)
	driver.downloadWarmupDone.Store(downloadURL, struct{}{})
	entry := drive.Entry{ID: "fid-1", Name: "file.bin", Size: 4096}
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       failingReadBody{},
			Request:    req,
		}, nil
	})

	before := driver.cl.downloadHTTP()
	rc, err := driver.Read(context.Background(), entry, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err == nil {
		t.Fatal("expected the injected body failure")
	}
	rc.Close()
	driver.cl.downloadMu.RLock()
	dirty := driver.cl.downloadDirty
	driver.cl.downloadMu.RUnlock()
	if !dirty {
		t.Fatal("body failure did not mark the download connection stale")
	}

	// The next request swaps generations instead of reusing the failed one.
	after := driver.cl.downloadHTTP()
	if after == before {
		t.Fatal("download transport was not rotated after a body failure")
	}

	// A seek cancels prefetch windows frequently; rotation is throttled so a
	// healthy pool is not discarded on every cancel.
	driver.cl.markDownloadFailed("another failed body")
	if got := driver.cl.downloadHTTP(); got != after {
		t.Fatal("download transport rotated again within the throttle window")
	}

	driver.cl.downloadMu.Lock()
	driver.cl.downloadRotated = time.Now().Add(-downloadRotateMinInterval - time.Second)
	driver.cl.downloadMu.Unlock()
	driver.cl.downloadMu.RLock()
	dirty = driver.cl.downloadDirty
	driver.cl.downloadMu.RUnlock()
	if !dirty {
		t.Fatal("throttled failure lost the stale mark")
	}
	if got := driver.cl.downloadHTTP(); got == after {
		t.Fatal("download transport did not rotate after the throttle window")
	}
}

// TestNewDownloadHTTPClientBoundsHeaderWait: a dead CDN edge must not consume
// the retry budget in 30-second header waits.
func TestNewDownloadHTTPClientBoundsHeaderWait(t *testing.T) {
	driver := New("k=v", Options{})
	transport, ok := driver.cl.newDownloadHTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatal("download transport is not *http.Transport")
	}
	if transport.ResponseHeaderTimeout != downloadResponseHeaderTimeout {
		t.Fatalf("response header timeout = %s, want %s", transport.ResponseHeaderTimeout, downloadResponseHeaderTimeout)
	}
	if transport.ResponseHeaderTimeout > 15*time.Second {
		t.Fatalf("response header timeout = %s, want a fast failure", transport.ResponseHeaderTimeout)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// TestDriverRotatesDownloadTransportOnHeaderTimeout: a header timeout retries
// on a fresh connection instead of waiting on the same dead edge again.
func TestDriverRotatesDownloadTransportOnHeaderTimeout(t *testing.T) {
	driver := New("k=v", Options{})
	driver.cl.downloadClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, timeoutError{}
	})
	req, err := http.NewRequest(http.MethodGet, "https://download.test/file.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := driver.cl.downloadHTTP()
	if _, err := driver.cl.doDownload(req, "k=v"); err == nil {
		t.Fatal("expected the injected header timeout")
	}
	if driver.cl.downloadHTTP() == before {
		t.Fatal("download transport was not rotated after a header timeout")
	}
}

type pacedBody struct {
	remaining int
	delay     time.Duration
	err       error
}

func (b *pacedBody) Read(p []byte) (int, error) {
	time.Sleep(b.delay)
	if b.remaining <= 0 {
		if b.err != nil {
			return 0, b.err
		}
		return 0, io.EOF
	}
	n := min(len(p), 1024, b.remaining)
	b.remaining -= n
	return n, nil
}

func (b *pacedBody) Close() error { return nil }

// TestTraceReadCloserReportsBodyError: a body failure is reported once with
// the bytes delivered and the provider time spent, so the driver can decide
// whether the connection failed. A slow body must not be turned into a
// synthetic truncated response.
func TestTraceReadCloserReportsBodyError(t *testing.T) {
	bodyErr := errors.New("connection reset by peer")
	var reported error
	trace := &traceReadCloser{
		ReadCloser: &pacedBody{remaining: 1 << 10, err: bodyErr},
		size:       1 << 20,
		start:      time.Now(),
		onError:    func(err error, _ int64, _ time.Duration) { reported = err },
	}
	if _, err := io.Copy(io.Discard, trace); !errors.Is(err, bodyErr) {
		t.Fatalf("read error = %v, want the body error", err)
	}
	if !errors.Is(reported, bodyErr) {
		t.Fatalf("reported error = %v, want the body error", reported)
	}
}

// TestTraceReadCloserKeepsSlowBody: a body that keeps making progress far
// below any historical rate must be delivered in full. Slow is not broken, and
// aborting it would replay already-downloaded bytes.
func TestTraceReadCloserKeepsSlowBody(t *testing.T) {
	const total = 8 << 10
	trace := &traceReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("x", total))),
		size:       total,
		start:      time.Now().Add(-time.Minute),
		onError:    func(err error, _ int64, _ time.Duration) { t.Fatalf("slow body reported %v", err) },
	}
	data, err := io.ReadAll(trace)
	if err != nil {
		t.Fatalf("slow body read: %v", err)
	}
	if len(data) != total {
		t.Fatalf("slow body bytes = %d, want %d", len(data), total)
	}
}

// TestDriverNeverRotatesOnLifecycleCancel: seek, close, released prefetch, and
// the read layer's own bounds all cancel bodies. None of them is evidence of a
// bad edge, so none may replace the connection pool or penalize the address.
func TestDriverNeverRotatesOnLifecycleCancel(t *testing.T) {
	driver := New("k=v", Options{})
	stale := func(d *Driver) bool {
		d.cl.downloadMu.RLock()
		defer d.cl.downloadMu.RUnlock()
		return d.cl.downloadDirty
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("http: response body closed")} {
		driver.noteDownloadBodyError(context.Background(), "1.2.3.4:443", err)
		if stale(driver) {
			t.Fatalf("%v marked the connection stale", err)
		}
		if driver.cl.edges.isBad(mustAddr(t, "1.2.3.4"), time.Now()) {
			t.Fatalf("%v penalized the edge", err)
		}
	}

	network := New("k=v", Options{})
	network.noteDownloadBodyError(context.Background(), "", errors.New("connection reset by peer"))
	if !stale(network) {
		t.Fatal("a network failure did not mark the connection stale")
	}

	// The read layer abandons a stalled fetch and marks it with a cause. That
	// is a connection that is not delivering, so the retry must not replay on
	// it, even though the body itself reports a plain cancellation.
	stalled := New("k=v", Options{})
	stalledCtx, cancelStalled := context.WithCancelCause(context.Background())
	cancelStalled(drive.ReadIdleStall)
	stalled.noteDownloadBodyError(stalledCtx, "", context.Canceled)
	if !stale(stalled) {
		t.Fatal("a stalled fetch did not mark the connection stale")
	}
}

// TestTraceReadCloserReportsShortBody: a provider that closes the connection
// before the requested range is complete reports a clean EOF. The driver must
// still see it as a transfer failure so the retry uses a fresh connection.
func TestTraceReadCloserReportsShortBody(t *testing.T) {
	var reported error
	trace := &traceReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader("short")),
		size:       1 << 20,
		start:      time.Now(),
		onError:    func(err error, _ int64, _ time.Duration) { reported = err },
	}
	if _, err := io.ReadAll(trace); err != nil {
		t.Fatalf("short body read: %v", err)
	}
	if !errors.Is(reported, errDownloadShortBody) {
		t.Fatalf("reported error = %v, want the short-body sentinel", reported)
	}
}

// TestDriverKeepsSlowBodyWithoutRotation: a moving body that finishes must not
// be replayed, rotated, or penalized no matter how slow it was.
func TestDriverKeepsSlowBodyWithoutRotation(t *testing.T) {
	const total = 64 << 10
	driver := New("k=v", Options{})
	driver.setURL("fid-slow", "https://download.test/slow.bin")
	driver.downloadWarmupDone.Store("https://download.test/slow.bin", struct{}{})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes 0-%d/%d", total-1, total)}},
			Body:       &pacedBody{remaining: total, delay: 2 * time.Millisecond},
			Request:    req,
		}, nil
	})
	entry := drive.Entry{ID: "fid-slow", Name: "slow.bin", Size: total}
	rc, err := driver.Read(context.Background(), entry, 0, total)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("slow read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if len(data) != total {
		t.Fatalf("slow read bytes = %d, want %d", len(data), total)
	}
	rotations, reason := driver.cl.downloadDebugSnapshot()
	if rotations != 0 {
		t.Fatalf("slow read rotated the pool: %d (%s)", rotations, reason)
	}
}

// TestDriverLowDownloadLimitDoesNotRotate: a user download cap paces the
// consumer; it must never look like a provider stall.
func TestDriverLowDownloadLimitDoesNotRotate(t *testing.T) {
	// The limiter paces strictly at the configured rate, so the request size
	// sets the wall time (total / 8KiB/s). Keep it just large enough to show
	// the cap in effect without spending seconds asleep; the fasttest build
	// shrinks the fixture further.
	total := int64(testknobs.FixtureBytes(8 << 10))
	driver := New("k=v", Options{})
	driver.limiter = drive.NewBandwidthLimiter(drive.BandwidthLimits{DownloadBytesPerSecond: 8 << 10})
	driver.setURL("fid-cap", "https://download.test/cap.bin")
	driver.downloadWarmupDone.Store("https://download.test/cap.bin", struct{}{})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes 0-%d/%d", total-1, total)}},
			Body:       io.NopCloser(bytes.NewReader(make([]byte, total))),
			Request:    req,
		}, nil
	})
	entry := drive.Entry{ID: "fid-cap", Name: "cap.bin", Size: total}
	rc, err := driver.Read(context.Background(), entry, 0, total)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("capped read: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != total {
		t.Fatalf("capped read bytes = %d, want %d", len(data), total)
	}
	if rotations, reason := driver.cl.downloadDebugSnapshot(); rotations != 0 {
		t.Fatalf("capped read rotated the pool: %d (%s)", rotations, reason)
	}
}

func TestDriverReadKeepsLargeRangeSingleStream(t *testing.T) {
	const total = int64(16 << 20)
	const downloadURL = "https://download.test/file.bin"
	driver := New("k=v", Options{})
	driver.setURL("fid-1", downloadURL)
	driver.downloadWarmupDone.Store(downloadURL, struct{}{})
	var mu sync.Mutex
	var ranges []string
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		ranges = append(ranges, req.Header.Get("Range"))
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes 0-%d/%d", total-1, total)}},
			Body:       io.NopCloser(bytes.NewReader(make([]byte, total))),
			Request:    req,
		}, nil
	})

	entry := drive.Entry{ID: "fid-1", Name: "file.bin", Size: total}
	rc, err := driver.Read(context.Background(), entry, 0, total)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	mu.Lock()
	got := append([]string(nil), ranges...)
	mu.Unlock()
	want := fmt.Sprintf("bytes=0-%d", total-1)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("range requests = %v, want one %q", got, want)
	}
}

// TestDriverDownloadURLRetriesCancelledSharedFetch: a foreground read that
// joined a prefetch's in-flight URL fetch must not inherit the prefetch's
// cancellation when a seek drops that prefetch.
func TestDriverDownloadURLRetriesCancelledSharedFetch(t *testing.T) {
	const wantURL = "https://download.test/file.bin"
	driver := New("k=v", Options{})
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	driver.cl.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return nil, req.Context().Err()
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"status":200,"code":0,"data":[{"download_url":"` + wantURL + `"}]}`)),
			Request: req,
		}, nil
	})

	prefetchCtx, cancelPrefetch := context.WithCancel(context.Background())
	defer cancelPrefetch()
	prefetchErr := make(chan error, 1)
	go func() {
		_, err := driver.downloadURL(prefetchCtx, "fid-1")
		prefetchErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("download URL request did not start")
	}

	type result struct {
		value downloadURLResult
		err   error
	}
	live := make(chan result, 1)
	go func() {
		value, err := driver.downloadURL(context.Background(), "fid-1")
		live <- result{value: value, err: err}
	}()
	// Let the foreground caller join the in-flight fetch before it is aborted.
	time.Sleep(20 * time.Millisecond)

	cancelPrefetch()
	close(release)
	if err := <-prefetchErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("prefetch error = %v, want context.Canceled", err)
	}
	got := <-live
	if got.err != nil {
		t.Fatalf("foreground download URL error = %v, want a fresh fetch", got.err)
	}
	if got.value.url != wantURL {
		t.Fatalf("foreground download URL = %q, want %q", got.value.url, wantURL)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("download URL calls = %d, want the canceled fetch plus one retry", n)
	}
}

func TestDriverDownloadUsesCookieSnapshotAfterRotation(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "__puus=old" {
			t.Fatalf("download URL request cookie = %q, want old snapshot", got)
		}
		apiCalls++
		w.Header().Add("Set-Cookie", "__puus=new; Path=/")
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("__puus=old", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.InstallStateStore(store)
	var downloadCalls int
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		if got := req.Header.Get("Cookie"); got != "__puus=old" {
			t.Fatalf("CDN request cookie = %q, want URL snapshot", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("hello")),
			Request:    req,
		}, nil
	})

	for range 2 {
		rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 0, 5)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "hello" {
			t.Fatalf("body = %q, want hello", data)
		}
	}

	if apiCalls != 1 {
		t.Fatalf("download URL calls = %d, want 1", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("CDN download calls = %d, want 2", downloadCalls)
	}
	if got := driver.cl.cookieValue(); got != "__puus=new" {
		t.Fatalf("current cookie = %q, want rotated cookie", got)
	}
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "__puus=new" {
		t.Fatalf("state cookie = %q, want rotated cookie", state.Cookie)
	}
}

func TestDriverDownloadURLCoalescesConcurrentMisses(t *testing.T) {
	var calls int
	var callsMu sync.Mutex
	requestEntered := make(chan struct{}, 2)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		requestEntered <- struct{}{}
		<-release
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": "https://download.test/file.bin"},
			},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := driver.downloadURL(context.Background(), "fid-1")
			if err == nil && result.url != "https://download.test/file.bin" {
				err = fmt.Errorf("download URL = %q", result.url)
			}
			results <- err
		}()
	}
	close(start)
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("download URL request did not start")
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if calls != 1 {
		t.Fatalf("download URL calls = %d, want 1", calls)
	}
}

func TestDriverInvalidateURLDoesNotDeleteNewerValue(t *testing.T) {
	driver := New("k=v", Options{})
	driver.setURL("fid-1", "https://download.test/fresh")
	driver.invalidateURL("fid-1", "https://download.test/stale")
	if url, ok := driver.getURL("fid-1"); !ok || url != "https://download.test/fresh" {
		t.Fatalf("cached URL = %q/%t, want fresh/true", url, ok)
	}
	driver.invalidateURL("fid-1", "https://download.test/fresh")
	if _, ok := driver.getURL("fid-1"); ok {
		t.Fatal("matching stale URL was not invalidated")
	}
}

func TestDriverReadRefreshesURLAfterForbidden(t *testing.T) {
	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/file/download" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		apiCalls++
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": []map[string]any{
				{"download_url": fmt.Sprintf("https://download.test/file-%d.bin", apiCalls)},
			},
		})
	}))
	defer api.Close()

	var downloadCalls int
	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		downloadCalls++
		status := http.StatusPartialContent
		body := "fresh"
		if downloadCalls == 1 {
			status = http.StatusForbidden
			body = "expired"
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 100}, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh" {
		t.Fatalf("body = %q, want fresh", data)
	}
	if apiCalls != 2 {
		t.Fatalf("download URL calls = %d, want 2", apiCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("download calls = %d, want 2", downloadCalls)
	}
}

func TestDriverReadWarmsDownloadConnectionOnce(t *testing.T) {
	driver := New("k=v", Options{})
	driver.setURL("fid-1", "https://download.test/file.bin")
	var ranges []string
	driver.cl.downloadClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		rangeHeader := req.Header.Get("Range")
		ranges = append(ranges, rangeHeader)
		// Answer the full requested span: a short body is a provider failure,
		// and this test is about the warmup request, not truncation.
		body := bytes.Repeat([]byte("x"), rangeBodyLen(rangeHeader))
		copy(body, "hello")
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    req,
		}, nil
	})

	for _, offset := range []int64{4, 9} {
		rc, err := driver.Read(context.Background(), drive.Entry{ID: "fid-1", Name: "file.bin", Size: 2 << 20}, offset, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(rc)
		if readErr != nil || len(data) != 1<<20 || string(data[:5]) != "hello" {
			t.Fatalf("read at %d len=%d err=%v, want 1 MiB starting with hello", offset, len(data), readErr)
		}
		_ = rc.Close()
	}

	if got, want := ranges, []string{"bytes=0-0", "bytes=4-1048579", "bytes=9-1048584"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("download ranges = %v, want %v", got, want)
	}
	if got := metricCount(driver.cl.metricEvents(time.Time{}), "download_warmup"); got != 1 {
		t.Fatalf("download warmup metrics = %d, want 1", got)
	}
}

// rangeBodyLen returns the byte count a "bytes=first-last" header requests.
func rangeBodyLen(header string) int {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0
	}
	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return 0
	}
	start, errStart := strconv.ParseInt(first, 10, 64)
	end, errEnd := strconv.ParseInt(last, 10, 64)
	if errStart != nil || errEnd != nil || end < start {
		return 0
	}
	return int(end - start + 1)
}

func TestDriverDebugSnapshot(t *testing.T) {
	driver := New("k=v", Options{RootID: "root", RootPath: "/Docs"})
	snapshot, err := driver.DebugSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Driver != "quark" {
		t.Fatalf("driver = %q, want quark", snapshot.Driver)
	}
	if snapshot.Stats[drive.DebugStatRootID] != "root" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Stats[drive.DebugStatRootPath] != "/Docs" {
		t.Fatalf("unexpected stats: %+v", snapshot.Stats)
	}
	if snapshot.Extra[drive.DebugExtraCredentialSource] == nil {
		t.Fatalf("expected credential_source extra, got %+v", snapshot.Extra)
	}
	if _, ok := snapshot.Stats["download_rotations"]; !ok {
		t.Fatalf("missing download rotation stats: %+v", snapshot.Stats)
	}
	if _, ok := snapshot.Stats["penalized_edges"]; !ok {
		t.Fatalf("missing penalized edge stats: %+v", snapshot.Stats)
	}
}

func TestCookieUpdatePersistsState(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("k=v", Options{})
	driver.InstallStateStore(store)
	driver.cl.updateCookie("__puus", "new")
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Cookie, "__puus=new") {
		t.Fatalf("cookie state = %q, want __puus=new", state.Cookie)
	}
	if driver.cookieSource != "response" {
		t.Fatalf("cookieSource = %q, want response", driver.cookieSource)
	}
}

func TestLoadCookieStateOverridesConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    "stored=1; __puus=stored",
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	if got := driver.cl.cookieValue(); got != "stored=1; __puus=stored" {
		t.Fatalf("cookie = %q, want stored cookie", got)
	}
	if driver.cookieSource != "state" {
		t.Fatalf("cookieSource = %q, want state", driver.cookieSource)
	}
}

// TestLoadCookieStateCredentialBinding pins which persisted sessions may
// replace the configured credential: rotation state survives restarts only for
// the same configured cookie, while a replaced credential (another account,
// or a freshly exported cookie) always wins.
func TestLoadCookieStateCredentialBinding(t *testing.T) {
	const (
		accountA = "__uid=account-a; __puus=a"
		accountB = "__uid=account-b; __puus=b"
	)
	tests := []struct {
		name         string
		configCookie string
		state        cookieState
		wantCookie   string
		wantSource   string
	}{
		{
			name:         "adopts rotated session for the configured credential",
			configCookie: accountA,
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated", Seed: cookieFingerprint(accountA), Account: "account-a"},
			wantCookie:   "__uid=account-a; __puus=a-rotated",
			wantSource:   "state",
		},
		{
			name:         "adopts rotated session when config pairs were reordered",
			configCookie: "__puus=a; __uid=account-a",
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated", Seed: cookieFingerprint(accountA), Account: "account-a"},
			wantCookie:   "__uid=account-a; __puus=a-rotated",
			wantSource:   "state",
		},
		{
			name:         "rejects legacy state for another account",
			configCookie: accountB,
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated"},
			wantCookie:   accountB,
			wantSource:   "config",
		},
		{
			name:         "rejects seeded state after the account was switched",
			configCookie: accountB,
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated", Seed: cookieFingerprint(accountA), Account: "account-a"},
			wantCookie:   accountB,
			wantSource:   "config",
		},
		{
			name:         "rejects seeded state after the cookie was re-exported",
			configCookie: "__uid=account-a; __puus=a-fresh",
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated", Seed: cookieFingerprint(accountA), Account: "account-a"},
			wantCookie:   "__uid=account-a; __puus=a-fresh",
			wantSource:   "config",
		},
		{
			name:         "keeps state when no config cookie exists",
			configCookie: "",
			state:        cookieState{Cookie: "__uid=account-a; __puus=a-rotated", Seed: cookieFingerprint(accountA), Account: "account-a"},
			wantCookie:   "__uid=account-a; __puus=a-rotated",
			wantSource:   "state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := drive.NewFileStateStore(t.TempDir())
			if err := store.SaveJSON("quark_cookie.json", tt.state); err != nil {
				t.Fatal(err)
			}
			driver := New(tt.configCookie, Options{})
			driver.InstallStateStore(store)
			driver.loadCookieState()
			if got := driver.cl.cookieValue(); got != tt.wantCookie {
				t.Fatalf("cookie = %q, want %q", got, tt.wantCookie)
			}
			if got := driver.cookieSource; got != tt.wantSource {
				t.Fatalf("cookieSource = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

func TestSaveUpdatedCookieBindsSeedAndAccount(t *testing.T) {
	store := drive.NewFileStateStore(t.TempDir())
	configCookie := "__uid=account-a; __puus=config"
	driver := New(configCookie, Options{})
	driver.InstallStateStore(store)
	driver.cl.updateCookie("__puus", "rotated")

	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Seed != cookieFingerprint(configCookie) {
		t.Fatalf("seed = %q, want fingerprint of the config cookie", state.Seed)
	}
	if state.Account != "account-a" {
		t.Fatalf("account = %q, want account-a", state.Account)
	}
	if !strings.Contains(state.Cookie, "__puus=rotated") {
		t.Fatalf("cookie = %q, want rotated __puus", state.Cookie)
	}
}

// TestPersistedCookieStateKeepsAccountsSeparate guards the multi-account
// invariant: two mounts of the same driver with different cookies keep their
// own rotated session across a restart.
func TestPersistedCookieStateKeepsAccountsSeparate(t *testing.T) {
	configA := "__uid=account-a; __puus=a"
	configB := "__uid=account-b; __puus=b"
	storeA := drive.NewFileStateStore(t.TempDir())
	storeB := drive.NewFileStateStore(t.TempDir())

	firstA := New(configA, Options{})
	firstA.InstallStateStore(storeA)
	firstA.cl.updateCookie("__puus", "a-rotated")

	firstB := New(configB, Options{})
	firstB.InstallStateStore(storeB)
	firstB.cl.updateCookie("__puus", "b-rotated")

	reloadA := New(configA, Options{})
	reloadA.InstallStateStore(storeA)
	reloadA.loadCookieState()
	reloadB := New(configB, Options{})
	reloadB.InstallStateStore(storeB)
	reloadB.loadCookieState()

	if got := reloadA.cl.cookieValue(); got != "__uid=account-a; __puus=a-rotated" {
		t.Fatalf("account a cookie = %q after restart, want its own rotated session", got)
	}
	if got := reloadB.cl.cookieValue(); got != "__uid=account-b; __puus=b-rotated" {
		t.Fatalf("account b cookie = %q after restart, want its own rotated session", got)
	}
	if reloadA.cookieSource != "state" || reloadB.cookieSource != "state" {
		t.Fatalf("cookie sources = %q/%q, want state/state", reloadA.cookieSource, reloadB.cookieSource)
	}
}

func TestSharedCredentialSessionPropagatesRotation(t *testing.T) {
	session := newFakeCredentialSession("main")
	first := New("__uid=account-a; __puus=one", Options{})
	first.InstallCredentialSession(session)
	first.subscribeCredentialSession()
	second := New("__uid=account-a; __puus=one", Options{})
	second.InstallCredentialSession(session)
	second.subscribeCredentialSession()

	first.cl.updateCookie("__puus", "two")

	if got := second.cl.cookieValue(); !strings.Contains(got, "__puus=two") {
		t.Fatalf("sibling cookie = %q, want the rotated value", got)
	}
	if second.cookieSource != "credential" {
		t.Fatalf("sibling source = %q, want credential", second.cookieSource)
	}
	snapshot := session.Snapshot()
	if snapshot.Account != "account-a" || snapshot.Version != 1 {
		t.Fatalf("session snapshot = %+v, want account-a at version 1", snapshot)
	}
	if second.cred.Version() != snapshot.Version {
		t.Fatalf("sibling version = %d, want %d", second.cred.Version(), snapshot.Version)
	}
}

func TestRecoverFallsBackToConfigCookie(t *testing.T) {
	driver := New("__uid=account-a; __puus=config", Options{})
	driver.InstallCredentialSession(newFakeCredentialSession("main"))
	driver.cl.setCookie("__uid=account-a; __puus=stale")
	driver.cookie = "__uid=account-a; __puus=stale"

	if !driver.recoverCredential() {
		t.Fatal("recoverCredential should fall back to the configured cookie")
	}
	if got := driver.cl.cookieValue(); got != "__uid=account-a; __puus=config" {
		t.Fatalf("cookie = %q, want the configured cookie", got)
	}
	if driver.cookieSource != "config" {
		t.Fatalf("source = %q, want config", driver.cookieSource)
	}
}

func TestSharedCredentialSessionIgnoresAnotherAccount(t *testing.T) {
	session := newFakeCredentialSession("main")
	if _, err := session.Update(0, []byte(`{"cookie":"__uid=account-a; __puus=one"}`), "account-a"); err != nil {
		t.Fatal(err)
	}
	driver := New("__uid=account-b; __puus=config", Options{})
	driver.InstallCredentialSession(session)
	driver.subscribeCredentialSession()
	driver.loadCredentialSession()

	if got := driver.cl.cookieValue(); got != "__uid=account-b; __puus=config" {
		t.Fatalf("cookie = %q, want the configured account", got)
	}
}

func TestAuthFailureAdoptsSiblingRotation(t *testing.T) {
	session := newFakeCredentialSession("main")
	if _, err := session.Update(0, []byte(`{"cookie":"__uid=account-a; __puus=rotated"}`), "account-a"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var cookies []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		attempt := len(cookies)
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"list": []any{}}})
	}))
	defer api.Close()

	driver := New("__uid=account-a; __puus=stale", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.InstallCredentialSession(session)

	var resp sortResp
	if err := driver.cl.request(context.Background(), http.MethodGet, "/file/sort", map[string]string{"pdir_fid": "0"}, nil, &resp); err != nil {
		t.Fatalf("request after sibling rotation = %v, want success", err)
	}
	if len(cookies) != 2 {
		t.Fatalf("attempts = %d, want 2 (stale + adopted)", len(cookies))
	}
	if !strings.Contains(cookies[1], "__puus=rotated") {
		t.Fatalf("retry cookie = %q, want the adopted rotation", cookies[1])
	}
}

func TestAuthFailureWithoutNewerSessionSurfaces(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	driver := New("__uid=account-a; __puus=stale", Options{BaseURL: api.URL, V2URL: api.URL})
	driver.InstallCredentialSession(newFakeCredentialSession("main"))

	var resp sortResp
	err := driver.cl.request(context.Background(), http.MethodGet, "/file/sort", map[string]string{"pdir_fid": "0"}, nil, &resp)
	if err == nil {
		t.Fatal("request without a newer session should surface the auth failure")
	}
}

type fakeCredentialSession struct {
	name    string
	mu      sync.Mutex
	payload []byte
	version uint64
	account string
	subs    []func(drive.CredentialSnapshot)
}

func newFakeCredentialSession(name string) *fakeCredentialSession {
	return &fakeCredentialSession{name: name}
}

func (s *fakeCredentialSession) Name() string { return s.name }

func (s *fakeCredentialSession) Snapshot() drive.CredentialSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return drive.CredentialSnapshot{
		Payload: append([]byte(nil), s.payload...),
		Version: s.version,
		Account: s.account,
	}
}

func (s *fakeCredentialSession) Update(expected uint64, payload []byte, account string) (drive.CredentialSnapshot, error) {
	s.mu.Lock()
	if expected != s.version {
		snapshot := drive.CredentialSnapshot{Payload: append([]byte(nil), s.payload...), Version: s.version, Account: s.account}
		s.mu.Unlock()
		return snapshot, drive.ErrCredentialVersionConflict
	}
	s.payload = append([]byte(nil), payload...)
	s.account = account
	s.version++
	snapshot := drive.CredentialSnapshot{Payload: append([]byte(nil), s.payload...), Version: s.version, Account: s.account}
	subs := append([]func(drive.CredentialSnapshot){}, s.subs...)
	s.mu.Unlock()
	for _, fn := range subs {
		fn(snapshot)
	}
	return snapshot, nil
}

func (s *fakeCredentialSession) Subscribe(fn func(drive.CredentialSnapshot)) func() {
	s.mu.Lock()
	s.subs = append(s.subs, fn)
	s.mu.Unlock()
	return func() {}
}

func (s *fakeCredentialSession) Refresh(context.Context, func(drive.CredentialSnapshot) ([]byte, string, error)) (drive.CredentialSnapshot, error) {
	return drive.CredentialSnapshot{}, errors.New("refresh unsupported")
}

func TestOSSClientHasNoWholeRequestTimeout(t *testing.T) {
	client := newOSSClient()
	if client.Timeout != 0 {
		t.Fatalf("oss client timeout = %s, want no whole-request timeout", client.Timeout)
	}
}

func TestOSSRetryDelayBacksOffForNetworkOutages(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 30 * time.Second},
		{attempt: 0, want: 30 * time.Second},
		{attempt: 1, want: time.Minute},
		{attempt: 2, want: 2 * time.Minute},
		{attempt: 3, want: 2 * time.Minute},
	}
	for _, tt := range tests {
		if got := ossRetryDelay(tt.attempt); got != tt.want {
			t.Fatalf("ossRetryDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestQuarkUploadConflictIsRetryable(t *testing.T) {
	if nonRetryableUploadStatus(http.StatusConflict) {
		t.Fatal("409 conflict should not be marked non-retryable; OSS upload sessions can be stale")
	}
	if !nonRetryableUploadStatus(http.StatusBadRequest) {
		t.Fatal("400 bad request should remain non-retryable")
	}
}

func TestResumedUploadConflictIsRetryableAndBindingRoundTrips(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	driver := New("k=v", Options{})
	driver.InstallStateStore(store)

	// 409 冲突判定为会话失效且可重试（与 OSS 陈旧 uploadId 语义一致）。
	if !invalidResumedUploadSession(uploadStatusError{op: "upload part 1", status: http.StatusConflict}) {
		t.Fatal("409 conflict should invalidate a resumed upload session")
	}
	if drive.IsNonRetryable(uploadStatusError{op: "upload part 1", status: http.StatusConflict}) {
		t.Fatal("409 conflict should stay retryable")
	}

	// 绑定（quarkToken）经新 Index 持久化并可跨实例读回。
	key := session.Identity{ParentID: "parent", Name: "data.bin", Size: 1, Fingerprint: "md5sha1"}.Key()
	raw, err := json.Marshal(quarkToken{TaskID: "task-old", UploadID: "upload-old", ObjKey: "obj-old", UploadURL: "upload.example", AuthInfo: "auth", PartSize: 4 * 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.sessions.Create(key, raw); err != nil {
		t.Fatal(err)
	}
	reloaded := session.NewIndex(store, quarkSessionFile, session.IndexOptions{})
	binding, ok := reloaded.Get(key)
	if !ok {
		t.Fatal("expected binding to survive a new index instance")
	}
	var tok quarkToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.UploadID != "upload-old" || tok.TaskID != "task-old" {
		t.Fatalf("unexpected persisted token: %+v", tok)
	}
}

func TestDriverPutInstantUploadFinishes(t *testing.T) {
	var finishCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id": "task-1",
					"obj_key": "obj-1",
					"fid":     "pre-fid",
					"finish":  true,
				},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	driver := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	localPath := filepath.Join(t.TempDir(), "same.bin")
	if err := os.WriteFile(localPath, []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "same.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 6),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "same.bin" || entry.Size != 6 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("instant upload entry modtime is zero")
	}
}

func TestDriverPutMultipartUpload(t *testing.T) {
	var partsMu sync.Mutex
	parts := map[string][]byte{}
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-1" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			parts[r.URL.Query().Get("partNumber")] = data
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"etag-1", "etag-2", "etag-3"} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("complete body missing %s: %s", want, data)
				}
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	var authMu sync.Mutex
	var authCalls int
	var hashCalled bool
	var finishCalled bool
	content := []byte("abcdefgh")
	contentMD5 := md5.Sum(content)
	contentSHA1 := sha1.Sum(content)
	wantMD5 := fmt.Sprintf("%x", contentMD5[:])
	wantSHA1 := fmt.Sprintf("%x", contentSHA1[:])
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			authMu.Lock()
			authCalls++
			authMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)

	tmp := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(content))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "data.bin" || entry.Size != 8 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatal("multipart upload entry modtime is zero")
	}
	if got := len(parts); got != 3 {
		t.Fatalf("part count = %d, want 3", got)
	}
	if got := string(parts["1"]) + string(parts["2"]) + string(parts["3"]); got != "abcdefgh" {
		t.Fatalf("uploaded data = %q, want abcdefgh", got)
	}
	authMu.Lock()
	gotAuthCalls := authCalls
	authMu.Unlock()
	if gotAuthCalls != 4 {
		t.Fatalf("auth calls = %d, want 4", gotAuthCalls)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !completed {
		t.Fatal("oss complete was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
}

func TestDriverPutMultipartUploadResumesPersistedParts(t *testing.T) {
	var partsMu sync.Mutex
	partUploads := map[string]int{}
	failPart2 := true
	var completed bool
	// part2Started closes when the hanging part-2 PUT reaches the server. The
	// driver only issues it after part 1 was acknowledged and its ETag was
	// persisted, so the test interrupts the attempt at a point where the
	// resume state is guaranteed on disk instead of racing a wall clock.
	part2Started := make(chan struct{})
	var part2Once sync.Once
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-resume" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			partNumber := r.URL.Query().Get("partNumber")
			_, _ = io.ReadAll(r.Body)
			partsMu.Lock()
			partUploads[partNumber]++
			shouldFail := failPart2 && partNumber == "2"
			partsMu.Unlock()
			if partNumber == "2" {
				part2Once.Do(func() { close(part2Started) })
				if shouldFail {
					<-r.Context().Done()
					return
				}
			}
			w.Header().Set("Etag", "etag-"+partNumber)
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"etag-1", "etag-2", "etag-3"} {
				if !bytes.Contains(data, []byte(want)) {
					t.Fatalf("complete body missing %s: %s", want, data)
				}
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	var apiMu sync.Mutex
	var preCalls int
	var hashCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			apiMu.Lock()
			preCalls++
			apiMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-resume",
					"upload_id":  "upload-resume",
					"obj_key":    "obj-resume",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			apiMu.Lock()
			hashCalls++
			apiMu.Unlock()
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	content := []byte("abcdefghi")
	tmp := filepath.Join(t.TempDir(), "resume.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	md5Sum := md5.Sum(content)
	sha1Sum := sha1.Sum(content)
	source := drive.NewLocalReadOnlyFileSourceWithHashes(tmp, int64(len(content)), drive.SourceHashes{
		drive.HashMD5:  md5Sum[:],
		drive.HashSHA1: sha1Sum[:],
	})
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))

	first := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	first.InstallStateStore(store)
	routeOSSToTestServer(first.cl.ossClient, oss)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.PutSource(firstCtx, drive.UploadRequest{ParentID: "parent", Name: "resume.bin", Source: source})
		firstDone <- err
	}()
	select {
	case <-part2Started:
		// Part 1 is on disk and part 2 is in flight: interrupt here.
	case err := <-firstDone:
		t.Fatalf("first attempt ended before part 2 started: %v", err)
	case <-time.After(30 * time.Second):
		cancelFirst()
		t.Fatal("first attempt never reached part 2")
	}
	cancelFirst()
	if err := <-firstDone; err == nil {
		t.Fatal("first upload unexpectedly succeeded")
	}

	partsMu.Lock()
	failPart2 = false
	if partUploads["1"] != 1 {
		t.Fatalf("part 1 uploads after first attempt = %d, want 1", partUploads["1"])
	}
	partsMu.Unlock()
	// 模拟进程被杀：不调用 Drop，第二个 driver 只能读取磁盘上已经
	// 持久化的分片确认记录。

	second := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	second.InstallStateStore(store)
	routeOSSToTestServer(second.cl.ossClient, oss)
	entry, err := second.PutSource(context.Background(), drive.UploadRequest{ParentID: "parent", Name: "resume.bin", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.Size != int64(len(content)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !completed {
		t.Fatal("multipart upload was not completed")
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	// quark 无分片进度查询：跳过基于本地确认 ETag 记录，part 1 不再重传；
	// pre/hash 不重复调用。
	if partUploads["1"] != 1 {
		t.Fatalf("part 1 was re-uploaded on resume: count=%d", partUploads["1"])
	}
	if partUploads["2"] < 2 || partUploads["3"] != 1 {
		t.Fatalf("unexpected resumed upload counts: %+v", partUploads)
	}
	apiMu.Lock()
	defer apiMu.Unlock()
	if preCalls != 1 || hashCalls != 1 {
		t.Fatalf("pre/hash calls = %d/%d, want 1/1", preCalls, hashCalls)
	}
}

func TestDriverPutSourceUnderCryptHashesEncryptedSource(t *testing.T) {
	var uploadedCipher []byte
	var completed bool
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			uploadedCipher = append(uploadedCipher[:0], data...)
			w.Header().Set("Etag", "etag-1")
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte("etag-1")) {
				t.Fatalf("complete body missing etag-1: %s", data)
			}
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	plain := []byte("plain payload")
	plainMD5 := md5.Sum(plain)
	plainSHA1 := sha1.Sum(plain)
	plainMD5Hex := fmt.Sprintf("%x", plainMD5[:])
	plainSHA1Hex := fmt.Sprintf("%x", plainSHA1[:])
	var hashCalled bool
	var hashBody map[string]any
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-crypt",
					"upload_id":  "upload-crypt",
					"obj_key":    "obj-crypt",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] == plainMD5Hex || body["sha1"] == plainSHA1Hex {
				t.Fatalf("hash body used plaintext hash: %+v", body)
			}
			hashBody = body
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{})
	tmp := filepath.Join(t.TempDir(), "plain.bin")
	if err := os.WriteFile(tmp, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewLocalReadOnlyFileSource(tmp, int64(len(plain))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if bytes.Contains(uploadedCipher, plain) {
		t.Fatal("uploaded body contains plaintext")
	}
	cipherMD5 := md5.Sum(uploadedCipher)
	cipherSHA1 := sha1.Sum(uploadedCipher)
	wantMD5 := fmt.Sprintf("%x", cipherMD5[:])
	wantSHA1 := fmt.Sprintf("%x", cipherSHA1[:])
	if hashBody["md5"] != wantMD5 || hashBody["sha1"] != wantSHA1 {
		t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", hashBody, wantMD5, wantSHA1)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !completed {
		t.Fatal("oss complete was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
}

func TestDriverPutSourceUnderContentDedupCryptInstantUploadsByEncryptedHash(t *testing.T) {
	ossCalled := false
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ossCalled = true
		t.Fatalf("oss should not be called for hash-finished upload: %s %s", r.Method, r.URL)
	}))
	defer oss.Close()

	plain := []byte("same plaintext should instant upload when encrypted content hash already exists")
	cp, err := cryptpkg.NewRcloneCipher("password", "salt")
	if err != nil {
		t.Fatal(err)
	}
	encrypted := encryptForTest(t, cp, plain)
	encryptedMD5 := md5.Sum(encrypted)
	encryptedSHA1 := sha1.Sum(encrypted)
	wantMD5 := fmt.Sprintf("%x", encryptedMD5[:])
	wantSHA1 := fmt.Sprintf("%x", encryptedSHA1[:])
	var hashCalled bool
	var finishCalled bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-dedup",
					"upload_id":  "upload-dedup",
					"obj_key":    "obj-dedup",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 64},
			})
		case "/file/update/hash":
			hashCalled = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["md5"] != wantMD5 || body["sha1"] != wantSHA1 {
				t.Fatalf("unexpected encrypted hash body: %+v, want md5=%s sha1=%s", body, wantMD5, wantSHA1)
			}
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": true, "fid": "existing-fid"},
			})
		case "/file/upload/finish":
			finishCalled = true
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-existing-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	cryptDriver := cryptpkg.NewDriver(driver, cp, cryptpkg.DriverOptions{ContentDedup: true})
	entry, err := cryptDriver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "secret.bin",
		Source:   drive.NewBytesReadOnlyFileSource(plain),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-existing-fid" || entry.ParentID != "parent" || entry.Name != "secret.bin" || entry.Size != int64(len(plain)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !hashCalled {
		t.Fatal("hash update was not called")
	}
	if !finishCalled {
		t.Fatal("finish was not called")
	}
	if ossCalled {
		t.Fatal("oss was called")
	}
}

func encryptForTest(t *testing.T, cp cryptpkg.Cipher, plain []byte) []byte {
	t.Helper()
	plainSHA256 := sha256.Sum256(plain)
	nonce, err := cp.ContentDedupNonce(plainSHA256, int64(len(plain)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, 0, cp.EncryptedSize(int64(len(plain))))
	encrypted = append(encrypted, []byte(cryptpkg.FileMagic)...)
	encrypted = append(encrypted, nonce[:]...)
	for blockIndex, offset := uint64(0), 0; offset < len(plain); blockIndex, offset = blockIndex+1, offset+cryptpkg.BlockDataSize {
		end := offset + cryptpkg.BlockDataSize
		if end > len(plain) {
			end = len(plain)
		}
		block, err := cp.EncryptBlock(plain[offset:end], blockIndex, nonce)
		if err != nil {
			t.Fatal(err)
		}
		encrypted = append(encrypted, block...)
	}
	return encrypted
}

func TestDriverPutRespectsServerPartSize(t *testing.T) {
	var partsMu sync.Mutex
	var partSizes []int
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partsMu.Lock()
			partSizes = append(partSizes, len(data))
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 4 * 1024 * 1024},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)

	localPath := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(localPath, []byte(strings.Repeat("a", 12*1024*1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   drive.NewLocalReadOnlyFileSource(localPath, 12*1024*1024),
	}); err != nil {
		t.Fatal(err)
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	if len(partSizes) != 3 {
		t.Fatalf("part count = %d, want 3 (server 4MB part size); sizes=%v", len(partSizes), partSizes)
	}
	for i, got := range partSizes {
		if got != 4*1024*1024 {
			t.Fatalf("part %d size = %d, want %d", i+1, got, 4*1024*1024)
		}
	}
}

func TestDriverPutSourceStreamsMultipartWithoutPartSizedReads(t *testing.T) {
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Etag", "etag-"+r.URL.Query().Get("partNumber"))
		w.WriteHeader(http.StatusOK)
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-1",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 4 * 1024 * 1024},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	source := newTrackingReadOnlyFileSource(bytes.Repeat([]byte("a"), 4*1024*1024+17))
	if _, err := driver.PutSource(context.Background(), drive.UploadRequest{
		ParentID: "parent",
		Name:     "data.bin",
		Source:   source,
	}); err != nil {
		t.Fatal(err)
	}
	if got := source.maxRead(); got >= 1024*1024 {
		t.Fatalf("max source read buffer = %d, want streaming reads below 1MiB", got)
	}
}

func TestDriverUploadPartUsesNativeBandwidthLimiter(t *testing.T) {
	// The handlers run on httptest's own goroutines and must never call
	// t.Fatal/t.Fatalf: when the client's cancellation fires mid-request it
	// aborts the connection, the handler's response write fails, and a
	// test-fatal call from that goroutine fails the whole test (observed on GH
	// runners).
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A real OSS only acknowledges a part after reading every byte. The
		// server must not answer 200+ETag for an aborted body: net/http can
		// surface that response as a successful Do while a cancel is racing
		// the body write, which would make uploadPart return a nil error.
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 64*1024 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Etag", "etag-1")
		w.WriteHeader(http.StatusOK)
	}))
	defer oss.Close()

	authServed := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file/upload/auth" {
			select {
			case authServed <- struct{}{}:
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"auth_key":"auth-key"}}`))
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	routeOSSToTestServer(driver.cl.ossClient, oss)
	driver.InstallBandwidthLimiter(drive.NewBandwidthLimiter(drive.BandwidthLimits{UploadBytesPerSecond: 1}))

	// 64KiB at 1 B/s needs 18 hours: a part upload that finishes while the
	// limiter is armed can only mean the native limiter was bypassed.
	body := strings.Repeat("s", 64*1024)
	pre := &upPreResp{}
	pre.Data.TaskID = "task-1"
	pre.Data.UploadID = "upload-1"
	pre.Data.ObjKey = "obj"
	pre.Data.UploadURL = strings.TrimPrefix(oss.URL, "https://")
	pre.Data.Bucket = "bucket"
	pre.Data.AuthInfo = "auth-info"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := driver.uploadPart(ctx, pre, 1, int64(len(body)), func() (io.Reader, error) {
			return strings.NewReader(body), nil
		})
		done <- err
	}()

	// Measure the throttle rather than the error a deadline produces: whether
	// a canceled PUT surfaces as a deadline error or as the test server's 200
	// is a client/transport race (observed once on a GH ubuntu runner), and
	// reading that 200 as "the part uploaded" is exactly what would make this
	// test flaky. Wait for the auth round trip so the PUT is certainly in
	// flight, then require that the throttled body keeps the call busy.
	select {
	case <-authServed:
	case err := <-done:
		t.Fatalf("uploadPart returned %v before the part PUT started", err)
	case <-time.After(30 * time.Second):
		t.Fatal("uploadPart never reached the part PUT")
	}
	select {
	case err := <-done:
		t.Fatalf("uploadPart returned %v; a 1 B/s limiter cannot move 64KiB", err)
	case <-time.After(250 * time.Millisecond):
	}

	// Cancelling must abort the throttled read promptly instead of leaving the
	// part hanging on the limiter.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("uploadPart error = %v, want context canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("uploadPart did not stop after cancel")
	}
}

func TestOssURLWithBucketPrefixesHost(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "pds.quark.cn"
	pre.Data.ObjKey = "path/to/file.bin"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/path/to/file.bin"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithoutBucket(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = ""
	pre.Data.UploadURL = "endpoint.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://endpoint.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

type trackingReadOnlyFileSource struct {
	data []byte
	mu   sync.Mutex
	max  int
}

func newTrackingReadOnlyFileSource(data []byte) *trackingReadOnlyFileSource {
	return &trackingReadOnlyFileSource{data: append([]byte(nil), data...)}
}

func (s *trackingReadOnlyFileSource) Size() int64 {
	return int64(len(s.data))
}

func (s *trackingReadOnlyFileSource) Open(ctx context.Context) (drive.ReadOnlyFile, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &trackingReadOnlyFile{source: s, reader: bytes.NewReader(s.data)}, nil
}

func (s *trackingReadOnlyFileSource) Hash(algorithm drive.HashAlgorithm) ([]byte, bool) {
	switch algorithm {
	case drive.HashMD5:
		sum := md5.Sum(s.data)
		return sum[:], true
	case drive.HashSHA1:
		sum := sha1.Sum(s.data)
		return sum[:], true
	default:
		return nil, false
	}
}

func (s *trackingReadOnlyFileSource) recordRead(size int) {
	s.mu.Lock()
	if size > s.max {
		s.max = size
	}
	s.mu.Unlock()
}

func (s *trackingReadOnlyFileSource) maxRead() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.max
}

type trackingReadOnlyFile struct {
	source *trackingReadOnlyFileSource
	reader *bytes.Reader
}

func (f *trackingReadOnlyFile) Read(p []byte) (int, error) {
	f.source.recordRead(len(p))
	return f.reader.Read(p)
}

func (f *trackingReadOnlyFile) ReadAt(p []byte, off int64) (int, error) {
	f.source.recordRead(len(p))
	return f.reader.ReadAt(p, off)
}

func (f *trackingReadOnlyFile) Seek(offset int64, whence int) (int64, error) {
	return f.reader.Seek(offset, whence)
}

func (f *trackingReadOnlyFile) Close() error {
	return nil
}

func TestOssURLStripsProtocol(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLStripsPathFromUploadURL(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "https://pds.quark.cn/some/path"
	pre.Data.ObjKey = "obj"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/obj"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func TestOssURLWithRealWorldExample(t *testing.T) {
	pre := &upPreResp{}
	pre.Data.Bucket = "ul-sz"
	pre.Data.UploadURL = "http://pds.quark.cn"
	pre.Data.ObjKey = "j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	got := ossURL(pre)
	want := "https://ul-sz.pds.quark.cn/j0uOasD0/5936150331/e410edbb116847b08f047d6474aa52396a3cefab/6a3cefab53760f59fd5b42be8a069dd70859b8b9"
	if got != want {
		t.Fatalf("ossURL = %q, want %q", got, want)
	}
}

func routeOSSToTestServer(c *http.Client, server *httptest.Server) {
	targetAddr := strings.TrimPrefix(server.URL, "https://")
	baseTransport := c.Transport.(*http.Transport)
	baseTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	baseDial := baseTransport.DialContext
	baseTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasSuffix(addr, targetAddr) {
			addr = targetAddr
		}
		return baseDial(ctx, network, addr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func metricCount(events []drive.MetricEvent, operation string) int {
	var count int
	for _, event := range events {
		if event.Operation == operation {
			count++
		}
	}
	return count
}

func TestValidateWithFallbackFallsBackToConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()
	if driver.cookie != "stored=1" {
		t.Fatalf("precondition: cookie = %q, want stored state cookie", driver.cookie)
	}

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "config=1", func() error {
		calls++
		if calls == 1 {
			return want // state cookie fails
		}
		return nil // config cookie succeeds
	})
	if err != nil {
		t.Fatalf("validateWithFallback error = %v, want nil after config fallback", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (state + config retry)", calls)
	}
	if driver.cookie != "config=1" {
		t.Fatalf("cookie = %q, want config cookie after fallback", driver.cookie)
	}
	var state cookieState
	if err := store.LoadJSON("quark_cookie.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Cookie != "config=1" {
		t.Fatalf("persisted cookie = %q, want working config cookie", state.Cookie)
	}
}

func TestValidateWithFallbackBothFailReturnsError(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("config=1", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "config=1", func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("validateWithFallback error = %v, want %v", err, want)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (state and config both fail)", calls)
	}
}

func TestValidateWithFallbackNoFallbackWithoutConfigCookie(t *testing.T) {
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))
	if err := store.SaveJSON("quark_cookie.json", cookieState{Cookie: "stored=1"}); err != nil {
		t.Fatal(err)
	}
	driver := New("", Options{})
	driver.InstallStateStore(store)
	driver.loadCookieState()

	want := errors.New("bad cookie")
	calls := 0
	err := driver.validateWithFallback(context.Background(), "", func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("validateWithFallback error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no config cookie to retry with)", calls)
	}
}

func TestDriverSpaceParsesEnvelope(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/member" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSON(t, w, map[string]any{
			"status": 200,
			"code":   0,
			"data": map[string]any{
				"total_capacity": int64(10995116277760), // 10 TiB
				"use_capacity":   int64(1234567890),
			},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	space, err := driver.Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if space.Total != 10995116277760 {
		t.Fatalf("total = %d, want 10995116277760 (capacity lives under data envelope)", space.Total)
	}
	if want := int64(10995116277760 - 1234567890); space.Free != want {
		t.Fatalf("free = %d, want %d", space.Free, want)
	}
}

func TestDriverSpaceReportsAPIError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"status":  400,
			"code":    23001,
			"message": "not found",
			"data":    map[string]any{},
		})
	}))
	defer api.Close()

	driver := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	if _, err := driver.Space(context.Background()); err == nil {
		t.Fatal("Space must surface the API error envelope")
	}
}

func TestDriverServerSideCopy(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/copy":
			calls = append(calls, "copy")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("copy body: %v", err)
			}
			if body["action_type"] != float64(1) || body["to_pdir_fid"] != "dst-dir" {
				t.Fatalf("copy body = %#v", body)
			}
			filelist, ok := body["filelist"].([]any)
			if !ok || len(filelist) != 1 || filelist[0] != "src-fid" {
				t.Fatalf("copy filelist = %#v", body["filelist"])
			}
			writeJSON(t, w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"task_id": ""}})
		case "/file/sort":
			calls = append(calls, "list")
			writeJSON(t, w, map[string]any{
				"status":   200,
				"code":     0,
				"metadata": map[string]any{"total": 1},
				"data": map[string]any{
					"list": []map[string]any{
						{"fid": "copied-fid", "file_name": "dst.txt", "file": false, "size": 4},
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	d := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	dst, err := d.Copy(context.Background(), drive.Entry{ID: "src-fid", Name: "src.txt", Size: 4}, "dst-dir", "dst.txt")
	if err != nil {
		t.Fatal(err)
	}
	if dst.ID != "copied-fid" || dst.Name != "dst.txt" {
		t.Fatalf("copy entry = %+v, want located id", dst)
	}
	if len(calls) != 2 || calls[0] != "copy" || calls[1] != "list" {
		t.Fatalf("calls = %v, want [copy list]", calls)
	}
}

func TestDriverServerSideCopyRejectsDirectory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	d := New("k=v", Options{BaseURL: server.URL, V2URL: server.URL})
	if _, err := d.Copy(context.Background(), drive.Entry{ID: "d", IsDir: true}, "0", "x"); err == nil {
		t.Fatal("directory copy should fail")
	}
}

func TestPutSourceHandlelessBindingIsDiscarded(t *testing.T) {
	var partsMu sync.Mutex
	partUploads := map[string]int{}
	var completed bool
	preCalls := 0
	oss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/obj-empty" {
			t.Fatalf("unexpected oss path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodPut:
			partNumber := r.URL.Query().Get("partNumber")
			_, _ = io.ReadAll(r.Body)
			partsMu.Lock()
			partUploads[partNumber]++
			partsMu.Unlock()
			w.Header().Set("Etag", "etag-"+partNumber)
			w.WriteHeader(http.StatusOK)
		case http.MethodPost:
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected oss method: %s", r.Method)
		}
	}))
	defer oss.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/file/upload/pre":
			preCalls++
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data": map[string]any{
					"task_id":    "task-1",
					"upload_id":  "upload-1",
					"obj_key":    "obj-empty",
					"upload_url": strings.TrimPrefix(oss.URL, "https://"),
					"fid":        "pre-fid",
					"bucket":     "bucket",
					"callback":   json.RawMessage(`{}`),
					"auth_info":  "auth-info",
				},
				"metadata": map[string]any{"part_size": 3},
			})
		case "/file/upload/auth":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"auth_key": "auth-key"},
			})
		case "/file/update/hash":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"finish": false},
			})
		case "/file/upload/finish":
			writeJSON(t, w, map[string]any{
				"status": 200,
				"code":   0,
				"data":   map[string]any{"fid": "final-fid"},
			})
		default:
			t.Fatalf("unexpected api path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	content := []byte("abcdefghi")
	tmp := filepath.Join(t.TempDir(), "resume.bin")
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		t.Fatal(err)
	}
	md5Sum := md5.Sum(content)
	sha1Sum := sha1.Sum(content)
	source := drive.NewLocalReadOnlyFileSourceWithHashes(tmp, int64(len(content)), drive.SourceHashes{
		drive.HashMD5:  md5Sum[:],
		drive.HashSHA1: sha1Sum[:],
	})
	store := drive.NewFileStateStore(filepath.Join(t.TempDir(), "driver"))

	d := New("k=v", Options{BaseURL: api.URL, V2URL: api.URL})
	d.InstallStateStore(store)
	routeOSSToTestServer(d.cl.ossClient, oss)
	// 模拟“预留后未完成创建”的崩溃残留：空句柄绑定必须先作废重来。
	key := session.Identity{
		ParentID:    "parent",
		Name:        "resume.bin",
		Size:        int64(len(content)),
		Fingerprint: strings.ToLower(fmt.Sprintf("%x", md5Sum)) + strings.ToLower(fmt.Sprintf("%x", sha1Sum)),
	}.Key()
	if err := d.sessions.Create(key, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}

	entry, err := d.PutSource(context.Background(), drive.UploadRequest{ParentID: "parent", Name: "resume.bin", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "final-fid" || entry.Size != int64(len(content)) {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	// 空句柄没有被当作可恢复会话：pre 只执行一次（全新上传）。
	if preCalls != 1 {
		t.Fatalf("pre calls = %d, want 1", preCalls)
	}
	if !completed {
		t.Fatal("multipart upload was not completed")
	}
	partsMu.Lock()
	defer partsMu.Unlock()
	for part := 1; part <= 3; part++ {
		if partUploads[strconv.Itoa(part)] != 1 {
			t.Fatalf("part %d uploads = %d, want 1", part, partUploads[strconv.Itoa(part)])
		}
	}
	if bindings := d.sessions.List(); len(bindings) != 0 {
		t.Fatalf("binding should be deleted after success, got %d", len(bindings))
	}
}

func TestQuarkTokenJSONRoundTrip(t *testing.T) {
	want := quarkToken{
		TaskID:    "task-1",
		UploadID:  "upload-1",
		ObjKey:    "obj-1",
		UploadURL: "https://oss.example/",
		Fid:       "fid-1",
		Bucket:    "bucket",
		Callback:  json.RawMessage(`{"x":1}`),
		AuthInfo:  "auth-info",
		PartSize:  3,
		Etags:     map[int]string{1: "etag-1", 3: "etag-3"},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got quarkToken
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != want.TaskID || got.UploadID != want.UploadID || got.ObjKey != want.ObjKey ||
		got.UploadURL != want.UploadURL || got.Fid != want.Fid || got.Bucket != want.Bucket ||
		got.AuthInfo != want.AuthInfo || got.PartSize != want.PartSize {
		t.Fatalf("round trip lost handle fields: %+v", got)
	}
	if string(got.Callback) != string(want.Callback) {
		t.Fatalf("round trip callback = %s, want %s", got.Callback, want.Callback)
	}
	if len(got.Etags) != 2 || got.Etags[1] != "etag-1" || got.Etags[3] != "etag-3" {
		t.Fatalf("round trip etags = %+v, want part 1 and 3 retained", got.Etags)
	}
}
