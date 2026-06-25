package quark

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL    = "https://drive.quark.cn/1/clouddrive"
	defaultV2URL      = "https://drive.quark.cn/api/v2"
	defaultReferer    = "https://pan.quark.cn"
	defaultUserAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) quark-cloud-drive/2.5.20 Chrome/100.0.4896.160 Electron/18.3.5.4-b478491100 Safari/537.36 Channel/pckk_other_ch"
	httpMaxRetries    = 3
	ossMaxRetries     = 3
	ossMaxConcurrent  = 4
	partUploadWorkers = 1
)

type client struct {
	httpClient     *http.Client
	downloadClient *http.Client
	ossClient      *http.Client
	cookie         string
	baseURL        string
	v2URL          string
	mu             sync.RWMutex
	sem            chan struct{}
	mgmtSem        chan struct{}
	metaSem        chan struct{}
	ossSem         chan struct{}
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
	return &client{
		httpClient:     newHTTPClient(30 * time.Second),
		downloadClient: newHTTPClient(0),
		ossClient:      newOSSClient(),
		cookie:         cookie,
		baseURL:        baseURL,
		v2URL:          v2URL,
		sem:            make(chan struct{}, 200),
		mgmtSem:        make(chan struct{}, 500),
		metaSem:        make(chan struct{}, 500),
		ossSem:         make(chan struct{}, ossMaxConcurrent),
	}
}

type clientOptions struct {
	BaseURL string
	V2URL   string
}

func newHTTPClient(timeout time.Duration) *http.Client {
	return newHTTPClientWithResponseHeaderTimeout(timeout, 30*time.Second)
}

func newHTTPClientWithResponseHeaderTimeout(timeout, responseHeaderTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}
	c := &http.Client{Transport: transport}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

func newOSSClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
	}
}

func (c *client) cookieValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cookie
}

func (c *client) updateCookie(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := strings.Split(c.cookie, "; ")
	for i, part := range parts {
		if strings.HasPrefix(part, key+"=") {
			parts[i] = key + "=" + value
			c.cookie = strings.Join(parts, "; ")
			return
		}
	}
	parts = append(parts, key+"="+value)
	c.cookie = strings.Join(parts, "; ")
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

func retryBackoff(attempt int) time.Duration {
	base := time.Duration(500<<uint(attempt)) * time.Millisecond
	jitter := float64(75+(attempt*7)%50) / 100.0
	return time.Duration(float64(base) * jitter)
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
	if shouldRetryWithAltBase(err) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "(Status 404)") || strings.Contains(msg, "(Status 405)")
}

func (c *client) request(method, path string, query map[string]string, body, result any) error {
	bases := []string{c.baseURL}
	if isMgmtPath(path) && c.v2URL != c.baseURL {
		bases = append(bases, c.v2URL)
	}

	var lastErr error
	for _, base := range bases {
		err := c.doRequest(method, base, path, query, body, result)
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

func (c *client) doRequest(method, baseURL, path string, query map[string]string, body, result any) error {
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

	for attempt := 0; attempt <= httpMaxRetries; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			jsonBody, _ := json.Marshal(body)
			bodyReader = bytes.NewReader(jsonBody)
		}

		req, err := http.NewRequest(method, u.String(), bodyReader)
		if err != nil {
			return fmt.Errorf("create request failed: %w", err)
		}
		req.Header.Set("Cookie", c.cookieValue())
		req.Header.Set("Origin", "https://pan.quark.cn")
		req.Header.Set("Referer", defaultReferer)
		req.Header.Set("User-Agent", defaultUserAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < httpMaxRetries && retryableHTTPError(err) {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return fmt.Errorf("request failed: %w", err)
		}

		bodyBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response failed: %w", readErr)
		}
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "__puus" || cookie.Name == "__pus" {
				c.updateCookie(cookie.Name, cookie.Value)
			}
		}
		if retryableHTTPStatus(resp.StatusCode) && attempt < httpMaxRetries {
			time.Sleep(retryBackoff(attempt))
			continue
		}
		if result != nil {
			if err := json.Unmarshal(bodyBytes, result); err != nil {
				return fmt.Errorf("parse response failed: %w", err)
			}
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("API Error (Status %d): %s", resp.StatusCode, string(bodyBytes))
		}
		return nil
	}
	return fmt.Errorf("max retries exceeded")
}

func (c *client) doDownload(req *http.Request) (*http.Response, error) {
	req.Header.Set("Cookie", c.cookieValue())
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Referer", defaultReferer)
	return c.downloadClient.Do(req)
}
