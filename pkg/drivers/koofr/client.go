package koofr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const userAgent = "qrypt-koofr/0.1"

// errMountNotSynced is a provider-state error: an external mount whose backend
// is out of sync fails every operation with 500 FilesNotSynced. It persists,
// so the client never retries it as a transient 5xx.
var errMountNotSynced = errors.New("koofr: mount files are not synced")

var passwordEchoPattern = regexp.MustCompile(`(?i)("password"\s*:\s*")[^"]*(")`)

// apiResponse is one finished HTTP exchange.
type apiResponse struct {
	req      *http.Request
	resp     *http.Response
	body     []byte
	duration time.Duration
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

// error maps a non-2xx response onto a stable drive sentinel, appending the
// provider error code and requestId when the body carries the JSON envelope.
// Non-JSON bodies (the provider answers some mkdir failures with plain text)
// still produce a categorized, redacted error.
func (r *apiResponse) error(prefix string) error {
	if r == nil || r.resp == nil {
		return fmt.Errorf("%s: no response", prefix)
	}
	redacted := redactBody(r.body)
	var env apiErrorEnvelope
	_ = json.Unmarshal(r.body, &env)
	code := env.Error.Code
	base := drive.HTTPError(prefix, r.req, r.resp, redacted)
	switch code {
	case "FilesNotSynced":
		return fmt.Errorf("%w: %v", errMountNotSynced, base)
	case "InvalidPath":
		return fmt.Errorf("%w: %v", drive.ErrInvalidInput, base)
	case "AlreadyExists", "Conflict":
		return fmt.Errorf("%w: %v", drive.ErrConflict, base)
	}
	if env.RequestID != "" {
		base = fmt.Errorf("%w (requestId %s)", base, env.RequestID)
	}
	return base
}

// redactBody masks share/app password echoes before generic redaction, so a
// password/set response body can never reach an error string.
func redactBody(body []byte) []byte {
	if len(body) == 0 || !bytes.Contains(body, []byte("password")) {
		return body
	}
	return passwordEchoPattern.ReplaceAll(body, []byte(`${1}<masked>${2}`))
}

type client struct {
	base     string
	email    string
	password string
	http     *http.Client
	metrics  *driverutil.Buffer
	wait     func(ctx context.Context, attempt int) error

	lastRequestID atomic.Value // string
}

func newClient(opts Options) *client {
	base := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
	if base == "" {
		base = defaultAPIBase
	}
	return &client{
		base:     base,
		email:    opts.Email,
		password: opts.Password,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 5 * time.Minute,
			},
			Timeout: 15 * time.Minute,
		},
		metrics: driverutil.NewBuffer(1000),
		wait:    util.WaitExponential,
	}
}

// api builds a metadata-prefixed endpoint (/api/v2.1).
func (c *client) api(path string) string { return c.base + "/api/v2.1" + path }

// content builds a content-prefixed endpoint (/content/api/v2.1).
func (c *client) content(path string) string { return c.base + "/content/api/v2.1" + path }

// do executes one logical request with retries on transport errors, 429, and
// 5xx. FilesNotSynced 5xx responses are never retried because the probe
// showed the condition persists for out-of-sync external mounts.
func (c *client) do(ctx context.Context, method, rawURL string, body []byte, contentType string, retry bool) (*apiResponse, error) {
	attempts := 1
	if retry {
		attempts = 3
	}
	var last *apiResponse
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.SetBasicAuth(c.email, c.password)
		start := time.Now()
		resp, err := c.http.Do(req)
		duration := time.Since(start)
		if err != nil {
			c.record(ctx, method, rawURL, 0, duration, err)
			if ctx.Err() != nil || attempt == attempts-1 || !driverutil.RetryableTransportError(err) {
				return nil, err
			}
			if err := c.wait(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		last = &apiResponse{req: req, resp: resp, body: data, duration: duration}
		c.record(ctx, method, rawURL, resp.StatusCode, duration, nil)
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		var env apiErrorEnvelope
		_ = json.Unmarshal(data, &env)
		if env.RequestID != "" {
			c.lastRequestID.Store(env.RequestID)
		}
		if attempt < attempts-1 && driverutil.RetryableStatus(resp.StatusCode) && env.Error.Code != "FilesNotSynced" {
			if err := c.wait(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}
		return last, nil
	}
}

// get performs a GET against the metadata prefix and decodes a JSON body.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) (*apiResponse, error) {
	target := c.api(path)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	resp, err := c.do(ctx, http.MethodGet, target, nil, "", true)
	if err != nil {
		return nil, err
	}
	if !resp.ok() {
		return resp, resp.error("koofr: GET " + path)
	}
	if out != nil && len(resp.body) > 0 {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return resp, fmt.Errorf("koofr: decode %s: %w", path, err)
		}
	}
	return resp, nil
}

// send performs a JSON request with any method and decodes a JSON body.
func (c *client) send(ctx context.Context, method, path string, query url.Values, payload, out any, retry bool) (*apiResponse, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("koofr: encode request: %w", err)
		}
		body = encoded
	}
	target := c.api(path)
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	resp, err := c.do(ctx, method, target, body, "application/json", retry)
	if err != nil {
		return nil, err
	}
	if !resp.ok() {
		return resp, resp.error("koofr: " + method + " " + path)
	}
	if out != nil && len(resp.body) > 0 {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return resp, fmt.Errorf("koofr: decode %s: %w", path, err)
		}
	}
	return resp, nil
}

func (c *client) post(ctx context.Context, path string, query url.Values, payload, out any) (*apiResponse, error) {
	return c.send(ctx, http.MethodPost, path, query, payload, out, true)
}

func (c *client) put(ctx context.Context, path string, query url.Values, payload, out any) (*apiResponse, error) {
	return c.send(ctx, http.MethodPut, path, query, payload, out, true)
}

func (c *client) delete(ctx context.Context, path string, query url.Values, out any) (*apiResponse, error) {
	return c.send(ctx, http.MethodDelete, path, query, nil, out, true)
}

func (c *client) record(ctx context.Context, method, rawURL string, status int, duration time.Duration, err error) {
	if c.metrics == nil {
		return
	}
	event := drive.MetricEvent{
		At:              time.Now(),
		Method:          method,
		URL:             sanitizeURL(rawURL),
		Status:          status,
		Duration:        duration.String(),
		SensitiveMasked: true,
	}
	if err != nil {
		event.Error = err.Error()
	}
	c.metrics.Record(ctx, event)
}

func sanitizeURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func (c *client) metricsSince(since time.Time) []drive.MetricEvent {
	if c.metrics == nil {
		return nil
	}
	return c.metrics.Events(since)
}

func (c *client) lastReqID() string {
	value, _ := c.lastRequestID.Load().(string)
	return value
}
