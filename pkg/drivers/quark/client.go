package quark

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httpclient"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httputil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const (
	defaultBaseURL    = "https://drive.quark.cn/1/clouddrive"
	defaultV2URL      = "https://drive.quark.cn/api/v2"
	defaultReferer    = "https://pan.quark.cn"
	defaultUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
	httpMaxRetries    = 3
	ossMaxRetries     = 3
	ossRequestTimeout = 2 * time.Minute
	ossRetryBaseDelay = 30 * time.Second
	ossMaxConcurrent  = 4
	partUploadWorkers = 1
)

// retrySleep pauses between transient request failures. Tests replace it to
// collapse the backoff so a simulated provider outage does not cost seconds of
// wall clock; the retry loop and its attempt counting stay exercised.
var retrySleep = func(attempt int) { time.Sleep(util.ExponentialBackoff(attempt)) }

// retryWait pauses between retryable download attempts. Like retrySleep it is a
// variable so tests can collapse the backoff; production keeps WaitExponential.
var retryWait = util.WaitExponential

type client struct {
	httpClient *http.Client
	// downloadClient is swapped when a download connection goes bad; reads go
	// through downloadHTTP so a degraded edge does not stick to every later
	// Range request.
	downloadClient  *http.Client
	downloadMu      sync.RWMutex
	downloadDirty   bool
	downloadReason  string
	downloadRotated time.Time
	// downloadRotations and downloadLastReason are diagnostic counters for
	// tests and /v1/driver: they show whether a failure actually swapped the
	// connection pool instead of only being throttled.
	downloadRotations  int
	downloadLastReason string
	ossClient          *http.Client
	cookie             string
	baseURL            string
	v2URL              string
	mu                 sync.RWMutex
	sem                chan struct{}
	mgmtSem            chan struct{}
	metaSem            chan struct{}
	ossSem             chan struct{}
	onCookieUpdate     func(cookie string)
	// onAuthRetry reloads a newer shared credential after a 401 and reports
	// whether the request should be retried once.
	onAuthRetry func() bool
	metrics     *driverutil.Buffer
	// wgKeys overrides the built-in wg data-key table for offline request
	// encryption (secureNo → 16-byte hex key).
	wgKeys map[int]string
	// edges tracks recently failed CDN edge addresses so download dials can
	// avoid them.
	edges *edgeHealth
}

func newClient(cookie string, opts clientOptions) *client {
	baseURL := strings.TrimRight(opts.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	v2URL := strings.TrimRight(opts.V2URL, "/")
	if v2URL == "" {
		v2URL = defaultV2URL
	}
	c := &client{
		httpClient: newHTTPClient(30 * time.Second),
		ossClient:  newOSSClient(),
		cookie:     cookie,
		baseURL:    baseURL,
		v2URL:      v2URL,
		sem:        make(chan struct{}, 200),
		mgmtSem:    make(chan struct{}, 500),
		metaSem:    make(chan struct{}, 500),
		ossSem:     make(chan struct{}, ossMaxConcurrent),
		metrics:    driverutil.NewBuffer(500),
		wgKeys:     opts.WGKeys,
	}
	c.edges = newEdgeHealth(edgePenaltyTTL)
	c.downloadClient = c.newDownloadHTTPClient()
	return c
}

type clientOptions struct {
	BaseURL string
	V2URL   string
	WGKeys  map[int]string
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return httputil.NewClient(timeout, 30*time.Second)
}

// downloadResponseHeaderTimeout bounds how long the CDN may take to send
// response headers. A dead edge otherwise burns the whole retry budget on
// 30-second waits before the read reaches its own budget (~2 minutes of
// "driver_open" latency measured on a real mount).
const downloadResponseHeaderTimeout = 10 * time.Second

// newDownloadHTTPClient keeps no whole-request timeout: reads are bounded by
// the read domain's idle/total fetch budgets so a large Range can stream.
// The dialer skips CDN edges that recently failed.
func (c *client) newDownloadHTTPClient() *http.Client {
	transport := httputil.DefaultTransport(downloadResponseHeaderTimeout)
	if c.edges != nil {
		transport.DialContext = c.edges.dial(transport.DialContext)
	}
	return &http.Client{Transport: transport}
}

func newOSSClient() *http.Client {
	return &http.Client{Transport: httputil.DefaultTransport(30 * time.Second)}
}

func (c *client) cookieValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cookie
}

func (c *client) updateCookie(key, value string) {
	c.mu.Lock()
	parts := strings.Split(c.cookie, "; ")
	for i, part := range parts {
		if strings.HasPrefix(part, key+"=") {
			parts[i] = key + "=" + value
			c.cookie = strings.Join(parts, "; ")
			updated := c.cookie
			c.mu.Unlock()
			c.notifyCookieUpdate(updated)
			return
		}
	}
	parts = append(parts, key+"="+value)
	c.cookie = strings.Join(parts, "; ")
	updated := c.cookie
	c.mu.Unlock()
	c.notifyCookieUpdate(updated)
}

func (c *client) setCookie(cookie string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cookie != "" {
		c.cookie = cookie
	}
}

func (c *client) notifyCookieUpdate(cookie string) {
	if c.onCookieUpdate != nil {
		c.onCookieUpdate(cookie)
	}
}

func isMgmtPath(path string) bool {
	return strings.HasPrefix(path, "/file/delete") ||
		strings.HasPrefix(path, "/file/rename") ||
		strings.HasPrefix(path, "/file/move") ||
		strings.HasPrefix(path, "/file/upload/commit") ||
		strings.HasPrefix(path, "/file/upload/finish")
}

func isMetaPath(path string) bool {
	return strings.HasPrefix(path, "/file/list") ||
		strings.HasPrefix(path, "/file/sort") ||
		strings.HasPrefix(path, "/file/search")
}

func retryableHTTPError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "connection refused")
}

func retryableHTTPStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// isWGResponse reports whether a response body is wg-encrypted. The marker is
// either the client's own X-U-Content-Encoding header or a Content-Encoding of
// "wg".
func isWGResponse(header http.Header) bool {
	for _, key := range []string{"X-U-Content-Encoding", "Content-Encoding"} {
		if strings.EqualFold(strings.TrimSpace(header.Get(key)), "wg") {
			return true
		}
	}
	return false
}

func shouldRetryWithAltBase(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup ")
}

func tryNextMgmtBase(err error) bool {
	if errors.Is(err, drive.ErrNotFound) {
		return false // a classified not-found is deterministic; no alt-base retry
	}
	if shouldRetryWithAltBase(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "(Status 404)") || strings.Contains(msg, "(Status 405)")
}

func (c *client) request(ctx context.Context, method, path string, query map[string]string, body, result any) error {
	bases := []string{c.baseURL}
	if isMgmtPath(path) && c.v2URL != c.baseURL {
		bases = append(bases, c.v2URL)
	}

	var lastErr error
	for _, base := range bases {
		err := c.doRequest(ctx, method, base, path, query, body, result, "")
		if err == nil {
			return nil
		}
		if isMgmtPath(path) && tryNextMgmtBase(err) {
			lastErr = err
			continue
		}
		return err
	}
	return lastErr
}

// requestWithCookie sends an API request with a fixed cookie snapshot. This is
// required when the response contains a signed download URL: the URL is bound
// to the cookie used to obtain it, while the response may rotate __puus.
func (c *client) requestWithCookie(ctx context.Context, method, path string, query map[string]string, body, result any, cookieSnapshot string) error {
	base := c.baseURL
	return c.doRequest(ctx, method, base, path, query, body, result, cookieSnapshot)
}

func (c *client) doRequest(ctx context.Context, method, baseURL, path string, query map[string]string, body, result any, cookieSnapshot string) error {
	start := time.Now()
	defer func() {
		logging.L.Infof("[QUARK] API %s %s dur=%s", method, path, time.Since(start))
	}()

	u, err := url.Parse(baseURL + path)
	if err != nil {
		return err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	q.Set("pr", "ucpro")
	q.Set("fr", "pc")
	u.RawQuery = q.Encode()

	sem := c.sem
	if isMgmtPath(path) {
		sem = c.mgmtSem
	} else if isMetaPath(path) {
		sem = c.metaSem
	}
	sem <- struct{}{}
	defer func() { <-sem }()

	encodedBody, isWG, err := encodeBody(body, c.wgKeys)
	if err != nil {
		return err
	}
	metricsBody := metricBody(body)

	for attempt := 0; attempt <= httpMaxRetries; attempt++ {
		req, err := httpclient.NewRawRequest(ctx, method, u.String(), encodedBody)
		if err != nil {
			return fmt.Errorf("create request failed: %w", err)
		}
		requestCookie := cookieSnapshot
		if requestCookie == "" {
			requestCookie = c.cookieValue()
		}
		req.Header.Set("Cookie", requestCookie)
		req.Header.Set("Origin", "https://pan.quark.cn")
		req.Header.Set("Referer", defaultReferer)
		req.Header.Set("User-Agent", defaultUserAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		if isWG {
			req.Header.Set("X-U-Content-Encoding", "wg")
		}

		httpStart := time.Now()
		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.recordMetric(ctx, drive.MetricEvent{
				Operation: path,
				Method:    req.Method,
				URL:       driverutil.URL(req.URL),
				Duration:  time.Since(httpStart).String(),
				Attempt:   attempt + 1,
				Retry:     attempt > 0,
				Request:   driverutil.MergeRequest(driverutil.RequestFields(query), driverutil.BodyFields(metricsBody)),
				Error:     err.Error(),
			})
			if attempt < httpMaxRetries && retryableHTTPError(err) {
				if cerr := ctx.Err(); cerr != nil {
					return fmt.Errorf("request failed: %w", err)
				}
				retrySleep(attempt)
				continue
			}
			return fmt.Errorf("request failed: %w", err)
		}

		bodyBytes, readErr := httpclient.ReadBody(resp)
		if readErr == nil && isWGResponse(resp.Header) {
			decoded, decryptErr := wgDecrypt(string(bodyBytes), c.wgKeys)
			if decryptErr != nil {
				return fmt.Errorf("decrypt wg response failed: %w", decryptErr)
			}
			bodyBytes = decoded
		}
		if readErr != nil {
			c.recordMetric(ctx, drive.MetricEvent{
				Operation: path,
				Method:    req.Method,
				URL:       driverutil.URL(req.URL),
				Status:    resp.StatusCode,
				Duration:  time.Since(httpStart).String(),
				Attempt:   attempt + 1,
				Retry:     attempt > 0,
				Request:   driverutil.MergeRequest(driverutil.RequestFields(query), driverutil.BodyFields(metricsBody)),
				Error:     readErr.Error(),
			})
			return fmt.Errorf("read response failed: %w", readErr)
		}
		event := drive.MetricEvent{
			Operation: path,
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(httpStart).String(),
			Attempt:   attempt + 1,
			Retry:     attempt > 0,
			Request:   driverutil.MergeRequest(driverutil.RequestFields(query), driverutil.BodyFields(metricsBody)),
			Response:  map[string]any{"bytes": len(bodyBytes)},
		}
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "__puus" || cookie.Name == "__pus" {
				c.updateCookie(cookie.Name, cookie.Value)
			}
		}
		if resp.StatusCode == http.StatusUnauthorized && cookieSnapshot == "" && attempt < 2 && c.onAuthRetry != nil && c.onAuthRetry() {
			c.recordMetric(ctx, event)
			continue
		}
		if retryableHTTPStatus(resp.StatusCode) && attempt < httpMaxRetries {
			c.recordMetric(ctx, event)
			retrySleep(attempt)
			continue
		}
		if result != nil {
			if err := httpclient.DecodeJSON(bodyBytes, result); err != nil {
				event.Error = err.Error()
				event.Response = map[string]any{"bytes": len(bodyBytes), "body_snippet": drive.Snippet(bodyBytes)}
				c.recordMetric(ctx, event)
				return fmt.Errorf("parse response failed: %w", err)
			}
		}
		if resp.StatusCode >= 400 {
			event.Response = map[string]any{"bytes": len(bodyBytes), "body_snippet": drive.Snippet(bodyBytes)}
			c.recordMetric(ctx, event)
			return drive.HTTPError("quark", req, resp, bodyBytes)
		}
		c.recordMetric(ctx, event)
		return nil
	}
	return fmt.Errorf("max retries exceeded")
}

// downloadHTTP returns the current download client generation. A failure from
// a previous body read is applied here, before the next request, so the retry
// that follows the failure cannot race the connection swap.
func (c *client) downloadHTTP() *http.Client {
	c.downloadMu.RLock()
	dirty := c.downloadDirty
	current := c.downloadClient
	c.downloadMu.RUnlock()
	if !dirty {
		return current
	}
	return c.rotateDownload()
}

// markDownloadFailed records that the current download connection produced a
// failed body read. The next request swaps to a fresh connection pool.
func (c *client) markDownloadFailed(reason string) {
	c.downloadMu.Lock()
	c.downloadDirty = true
	c.downloadReason = reason
	c.downloadMu.Unlock()
}

// downloadDebugSnapshot reports how many times the pool was replaced and the
// most recent reason. It is read by the driver debug snapshot.
func (c *client) downloadDebugSnapshot() (rotations int, lastReason string) {
	c.downloadMu.RLock()
	defer c.downloadMu.RUnlock()
	return c.downloadRotations, c.downloadLastReason
}

// downloadRotateMinInterval bounds how often a failure may replace the
// download transport: a seek cancels prefetch windows frequently and must not
// discard a healthy connection pool each time, while a real stall retries on
// the next read with a fresh connection.
const downloadRotateMinInterval = 5 * time.Second

// rotateDownload replaces the download client with a fresh connection pool and
// closes the previous generation's idle connections. Requests already in
// flight finish on the old pool; everything new avoids the degraded one.
func (c *client) rotateDownload() *http.Client {
	c.downloadMu.Lock()
	if !c.downloadDirty || (!c.downloadRotated.IsZero() && time.Since(c.downloadRotated) < downloadRotateMinInterval) {
		current := c.downloadClient
		c.downloadMu.Unlock()
		return current
	}
	old := c.downloadClient
	reason := c.downloadReason
	fresh := c.newDownloadHTTPClient()
	c.downloadClient = fresh
	c.downloadDirty = false
	c.downloadReason = ""
	c.downloadRotated = time.Now()
	c.downloadRotations++
	c.downloadLastReason = reason
	c.downloadMu.Unlock()
	if old != nil {
		if transport, ok := old.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	logging.L.Debugf("[QUARK] rotate download transport reason=%q", reason)
	return fresh
}

func (c *client) doDownload(req *http.Request, cookieSnapshot string) (*http.Response, error) {
	if cookieSnapshot == "" {
		cookieSnapshot = c.cookieValue()
	}
	req.Header.Set("Cookie", cookieSnapshot)
	// Quark's CDN applies different policies to browser-like download
	// requests. Keep the API client UA above, but make the signed CDN request
	// match the request used by the official web client/kuake_cli.
	req.Header.Set("User-Agent", downloadChromeUA)
	req.Header.Set("Referer", defaultReferer+"/")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Priority", "u=0, i")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="146", "Google Chrome";v="146", "Not_A Brand";v="24"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	resp, err := c.downloadHTTP().Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			// A header timeout means the edge is dead or overloaded; retrying
			// the same connection only repeats the wait.
			c.markDownloadFailed("download request timed out: " + err.Error())
			c.rotateDownload()
		}
	}
	return resp, err
}

func (c *client) recordMetric(ctx context.Context, event drive.MetricEvent) {
	c.metrics.Record(ctx, event)
}

func (c *client) metricEvents(since time.Time) []drive.MetricEvent {
	return c.metrics.Events(since)
}
