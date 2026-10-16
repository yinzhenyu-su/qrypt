package p123

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httpclient"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httputil"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

const (
	defaultBaseURL  = "https://yun.123pan.com/b/api"
	defaultLoginURL = "https://login.123pan.com/api/user/sign_in"

	originHeader  = "https://yun.123pan.com"
	refererHeader = "https://yun.123pan.com/"
	apiUserAgent  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	apiPlatform   = "web"
	apiVersion    = "3"

	httpMaxRetries = 3
	apiTimeout     = 60 * time.Second

	// 123pan throttles concurrent list traffic; keep list requests at least
	// this far apart while allowing the first page through immediately.
	listMinInterval = 700 * time.Millisecond
)

// signTable is the digit-substitution alphabet 123pan's web client uses when
// deriving its request signature.
const signTable = "adefghlmyijnopkqrstubcvwsz"

type client struct {
	httpClient     *http.Client
	uploadClient   *http.Client
	downloadClient *http.Client
	baseURL        string
	loginURL       string
	username       string
	password       string
	tokenMu        sync.RWMutex
	token          string
	loginMu        sync.Mutex
	listLimiter    *rate.Limiter
	onTokenUpdate  func(token string, expire time.Time)
	metrics        *driverutil.Buffer
	reloginCount   atomic.Int64
}

func newClient(username, password, baseURL string) *client {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &client{
		httpClient:     httputil.NewClient(apiTimeout, 30*time.Second),
		uploadClient:   httputil.NewClient(0, 60*time.Second),
		downloadClient: newNoRedirectClient(),
		baseURL:        base,
		loginURL:       defaultLoginURL,
		username:       username,
		password:       password,
		listLimiter:    rate.NewLimiter(rate.Every(listMinInterval), 1),
		metrics:        driverutil.NewBuffer(500),
	}
}

// newNoRedirectClient builds the CDN client. Download URLs answer with a JSON
// redirect envelope or a Location header that the driver resolves itself, so
// following redirects automatically would hide the status it needs.
func newNoRedirectClient() *http.Client {
	client := httputil.NewClient(0, 60*time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

func (c *client) tokenValue() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *client) setToken(token string) {
	if token == "" {
		return
	}
	c.tokenMu.Lock()
	c.token = token
	c.tokenMu.Unlock()
}

// signPath reproduces the web client's query signature. The signature key is a
// CRC32 of the minute-stamped time; the value binds the timestamp, a random
// nonce, the request path, platform, version and that time token.
func signPath(path, platform, version string, now time.Time) (string, string) {
	random := strconv.FormatInt(int64(math.Round(1e7*rand.Float64())), 10)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	timeSign := signKey(now)
	data := strings.Join([]string{timestamp, random, path, platform, version, timeSign}, "|")
	dataSign := strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(data))), 10)
	return timeSign, strings.Join([]string{timestamp, random, dataSign}, "-")
}

// signKey maps the minute-stamped China-time clock through 123pan's digit
// alphabet and returns the decimal CRC32 the server recomputes to validate the
// request.
func signKey(now time.Time) string {
	stamp := []byte(now.In(time.FixedZone("CST", 8*3600)).Format("200601021504"))
	for i, b := range stamp {
		stamp[i] = signTable[b-'0']
	}
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE(stamp)), 10)
}

func (c *client) buildURL(path string, query map[string]string) (string, error) {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return "", fmt.Errorf("123: build url: %w", err)
	}
	q := u.Query()
	for key, value := range query {
		q.Set(key, value)
	}
	key, value := signPath(u.Path, apiPlatform, apiVersion, time.Now())
	q.Add(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// login exchanges the configured credentials for a bearer token. Email
// accounts use the mail login shape; phone accounts use the passport shape.
func (c *client) login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.username == "" || c.password == "" {
		return fmt.Errorf("123: %w: no username/password configured for re-login", drive.ErrAuth)
	}
	body := map[string]any{"password": c.password}
	if strings.Contains(c.username, "@") {
		body["mail"] = c.username
		body["type"] = 2
	} else {
		body["passport"] = c.username
		body["remember"] = true
	}
	req, err := httpclient.NewJSONRequest(ctx, http.MethodPost, c.loginURL, body)
	if err != nil {
		return fmt.Errorf("123: login: %w", err)
	}
	req.Header.Set("origin", originHeader)
	req.Header.Set("referer", refererHeader)
	req.Header.Set("user-agent", apiUserAgent)
	req.Header.Set("platform", apiPlatform)
	req.Header.Set("app-version", apiVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("123: login: %w", err)
	}
	data, readErr := httpclient.ReadBody(resp)
	if readErr != nil {
		return fmt.Errorf("123: login: read response: %w", readErr)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return drive.HTTPError("123: login", req, resp, data)
	}
	var out loginResp
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("123: login: decode response: %w", err)
	}
	if out.Code != loginCodeOK || out.Data.Token == "" {
		return fmt.Errorf("123: %w: login rejected (code %d): %s", drive.ErrAuth, out.Code, drive.Snippet(data))
	}
	c.setToken(out.Data.Token)
	if c.onTokenUpdate != nil {
		c.onTokenUpdate(out.Data.Token, parseAPITime(out.Data.Expire))
	}
	return nil
}

func (c *client) roundTrip(ctx context.Context, method, path string, query map[string]string, body any) (*http.Request, *http.Response, []byte, error) {
	rawURL, err := c.buildURL(path, query)
	if err != nil {
		return nil, nil, nil, err
	}
	req, err := httpclient.NewJSONRequest(ctx, method, rawURL, body)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("origin", originHeader)
	req.Header.Set("referer", refererHeader)
	req.Header.Set("authorization", "Bearer "+c.tokenValue())
	req.Header.Set("user-agent", apiUserAgent)
	req.Header.Set("platform", apiPlatform)
	req.Header.Set("app-version", apiVersion)
	req.Header.Set("accept", "application/json, text/plain, */*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return req, nil, nil, err
	}
	data, readErr := httpclient.ReadBody(resp)
	if readErr != nil {
		return req, resp, nil, readErr
	}
	return req, resp, data, nil
}

// request performs one signed API call and decodes the JSON envelope into
// result. It retries transient transport/5xx/429 failures and re-logs in once
// when the token has expired.
func (c *client) request(ctx context.Context, method, path string, query map[string]string, body, result any) error {
	relogged := false
	for attempt := 0; attempt <= httpMaxRetries; attempt++ {
		started := time.Now()
		req, resp, data, err := c.roundTrip(ctx, method, path, query, body)
		event := drive.MetricEvent{
			Operation: path,
			Method:    method,
			Attempt:   attempt + 1,
			Retry:     attempt > 0,
			Duration:  time.Since(started).String(),
			Request:   driverutil.MergeRequest(driverutil.RequestFields(query), driverutil.BodyFields(body)),
		}
		if req != nil && req.URL != nil {
			event.URL = driverutil.URL(req.URL)
		}
		if err != nil {
			event.Error = err.Error()
			c.recordMetric(ctx, event)
			if attempt < httpMaxRetries && retryableError(err) {
				if waitErr := util.WaitExponential(ctx, attempt); waitErr != nil {
					return waitErr
				}
				continue
			}
			return fmt.Errorf("123: request %s: %w", path, err)
		}
		event.Status = resp.StatusCode
		event.Response = map[string]any{"bytes": len(data)}
		if retryableStatus(resp.StatusCode) && attempt < httpMaxRetries {
			event.Response["body_snippet"] = drive.Snippet(data)
			c.recordMetric(ctx, event)
			if waitErr := util.WaitExponential(ctx, attempt); waitErr != nil {
				return waitErr
			}
			continue
		}
		if resp.StatusCode >= http.StatusBadRequest {
			event.Response["body_snippet"] = drive.Snippet(data)
			c.recordMetric(ctx, event)
			return drive.HTTPError("123", req, resp, data)
		}

		var env apiEnvelope
		if len(data) > 0 {
			if err := json.Unmarshal(data, &env); err != nil {
				event.Error = err.Error()
				event.Response["body_snippet"] = drive.Snippet(data)
				c.recordMetric(ctx, event)
				return fmt.Errorf("123: decode response %s: %w", path, err)
			}
		}
		if env.Code == apiCodeAuth && !relogged {
			c.recordMetric(ctx, event)
			if err := c.login(ctx); err != nil {
				return err
			}
			c.reloginCount.Add(1)
			relogged = true
			attempt--
			continue
		}
		if err := apiError(env); err != nil {
			event.Error = err.Error()
			c.recordMetric(ctx, event)
			return err
		}
		if result != nil {
			if err := json.Unmarshal(data, result); err != nil {
				event.Error = err.Error()
				event.Response["body_snippet"] = drive.Snippet(data)
				c.recordMetric(ctx, event)
				return fmt.Errorf("123: decode response %s: %w", path, err)
			}
		}
		c.recordMetric(ctx, event)
		return nil
	}
	return fmt.Errorf("123: request %s: retries exhausted", path)
}

func apiError(env apiEnvelope) error {
	if env.Code == apiCodeOK {
		return nil
	}
	switch env.Code {
	case apiCodeAuth:
		return fmt.Errorf("123: %w: %s", drive.ErrAuth, env.Message)
	case apiCodeNotFound, apiCodeNoFile:
		return fmt.Errorf("123: %w: %s", drive.ErrNotFound, env.Message)
	case apiCodeDuplicate:
		return fmt.Errorf("%w: %s", errDuplicate, env.Message)
	case apiCodeInvalidRequest:
		return fmt.Errorf("123: %w: %s", drive.ErrInvalidInput, env.Message)
	case apiCodeRateLimit:
		return fmt.Errorf("123: %w: %s", drive.ErrRateLimit, env.Message)
	default:
		return fmt.Errorf("123: api error %d: %s", env.Code, env.Message)
	}
}

// errDuplicate marks a create collision (123pan code 5060): a same-name object
// already exists in the target directory.
var errDuplicate = errors.New("123: duplicate name")

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

func retryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
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
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "unexpected eof")
}

func (c *client) waitList(ctx context.Context) error {
	if c.listLimiter == nil {
		return nil
	}
	return c.listLimiter.Wait(ctx)
}

func (c *client) recordMetric(ctx context.Context, event drive.MetricEvent) {
	c.metrics.Record(ctx, event)
}

func (c *client) metricEvents(since time.Time) []drive.MetricEvent {
	return c.metrics.Events(since)
}
