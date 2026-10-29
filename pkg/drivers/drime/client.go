package drime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const userAgent = "qrypt-drime/0.1"

var retryWait = util.WaitExponential

type apiResponse struct {
	req      *http.Request
	resp     *http.Response
	body     []byte
	duration time.Duration
}

func (r *apiResponse) error(prefix string, opts ...drive.HTTPErrorOption) error {
	if r == nil {
		return fmt.Errorf("%s: empty response", prefix)
	}
	if r.resp == nil {
		return fmt.Errorf("%s: no response", prefix)
	}
	return drive.HTTPError(prefix, r.req, r.resp, r.body, opts...)
}

func (r *apiResponse) status() int {
	if r == nil || r.resp == nil {
		return 0
	}
	return r.resp.StatusCode
}

func (r *apiResponse) ok() bool {
	code := r.status()
	return code >= 200 && code < 300
}

func (r *apiResponse) header(name string) string {
	if r == nil || r.resp == nil {
		return ""
	}
	return r.resp.Header.Get(name)
}

type client struct {
	base         string
	downloadBase string
	workspaceID  string
	token        string
	http         *http.Client
	metrics      *driverutil.Buffer
	limiter      *driverutil.RateLimiter
}

func newClient(opts Options) (*client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
	if base == "" {
		base = defaultAPIBase
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("drime: invalid api_base_url %q: %w", base, drive.ErrInvalidInput)
	}
	downloadBase := u.Scheme + "://" + u.Host + "/"
	if strings.HasSuffix(base, "/api/v1") {
		downloadBase = strings.TrimSuffix(base, "/api/v1") + "/"
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}
	httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Minute}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("drime: too many redirects")
		}
		return nil
	}
	return &client{
		base:         base,
		downloadBase: downloadBase,
		workspaceID:  opts.WorkspaceID,
		token:        opts.AccessToken,
		http:         httpClient,
		metrics:      driverutil.NewBuffer(1000),
		limiter:      driverutil.NewRateLimiter(opts.LimitRate),
	}, nil
}

func (c *client) endpoint(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.base + path
}

func (c *client) doJSON(ctx context.Context, method, path string, payload any, auth bool) (*apiResponse, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("drime: encode request: %w", err)
		}
		body = encoded
	}
	retry := method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
	return c.do(ctx, method, path, body, "application/json", auth, retry)
}

func (c *client) do(ctx context.Context, method, path string, body []byte, contentType string, auth, retry bool) (*apiResponse, error) {
	policy := driverutil.RetryPolicy{MaxAttempts: 1}
	if retry {
		policy.MaxAttempts = 3
	}
	policy.Wait = func(ctx context.Context, attempt int, delay time.Duration) error {
		if delay > 0 {
			return waitContext(ctx, delay)
		}
		return retryWait(ctx, attempt)
	}
	var last *apiResponse
	resp, err := policy.Do(ctx, func(ctx context.Context, _ int) (*http.Response, error) {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if auth && c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		start := time.Now()
		resp, err := c.http.Do(req)
		duration := time.Since(start)
		last = &apiResponse{req: req, resp: resp, duration: duration}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		c.record(ctx, method, path, status, duration, err)
		return resp, err
	})
	if err != nil {
		return nil, err
	}
	if last == nil {
		return nil, fmt.Errorf("drime: request failed")
	}
	data, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	last.resp = resp
	last.body = data
	if readErr != nil {
		c.record(ctx, method, path, resp.StatusCode, last.duration, readErr)
		return nil, readErr
	}
	if closeErr != nil {
		c.record(ctx, method, path, resp.StatusCode, last.duration, closeErr)
		return nil, closeErr
	}
	return last, nil
}

func (c *client) doRaw(ctx context.Context, method, rawURL string, body io.Reader, contentLength int64, contentType string, auth bool) (*apiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	if auth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.limiter != nil && strings.HasPrefix(rawURL, c.base) {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	duration := time.Since(start)
	if err != nil {
		c.record(ctx, method, sanitizePath(rawURL), 0, duration, err)
		return nil, err
	}
	data, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	apiResp := &apiResponse{req: req, resp: resp, body: data}
	if readErr != nil {
		c.record(ctx, method, sanitizePath(rawURL), resp.StatusCode, duration, readErr)
		return nil, readErr
	}
	if closeErr != nil {
		c.record(ctx, method, sanitizePath(rawURL), resp.StatusCode, duration, closeErr)
		return nil, closeErr
	}
	c.record(ctx, method, sanitizePath(rawURL), resp.StatusCode, duration, nil)
	return apiResp, nil
}

func (c *client) get(ctx context.Context, path string, out any) (*apiResponse, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		return nil, err
	}
	if !resp.ok() {
		return resp, resp.error("drime: " + path)
	}
	if out != nil && len(resp.body) > 0 {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return resp, fmt.Errorf("drime: decode %s: %w", path, err)
		}
	}
	return resp, nil
}

func (c *client) post(ctx context.Context, path string, payload any, out any) (*apiResponse, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, path, payload, true)
	if err != nil {
		return nil, err
	}
	if !resp.ok() {
		return resp, resp.error("drime: " + path)
	}
	if out != nil && len(resp.body) > 0 {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return resp, fmt.Errorf("drime: decode %s: %w", path, err)
		}
	}
	return resp, nil
}

func (c *client) record(ctx context.Context, method, path string, status int, duration time.Duration, err error) {
	if c.metrics == nil {
		return
	}
	event := drive.MetricEvent{
		At:              time.Now(),
		Method:          method,
		URL:             sanitizePath(path),
		Status:          status,
		Duration:        duration.String(),
		SensitiveMasked: true,
	}
	if err != nil {
		event.Error = err.Error()
	}
	c.metrics.Record(ctx, event)
}

func sanitizePath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if u, err := url.Parse(path); err == nil && u.Scheme != "" {
		return u.Scheme + "://" + u.Host + u.Path
	}
	return path
}

func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *client) metricsSince(since time.Time) []drive.MetricEvent {
	if c.metrics == nil {
		return nil
	}
	return c.metrics.Events(since)
}
