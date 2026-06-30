package aliyundrive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/httputil"
)

const (
	defaultAPIBaseURL = "https://api.alipan.com"
	defaultAuthURL    = "https://auth.alipan.com/v2/account/token"
	defaultReferer    = "https://alipan.com/"
	defaultOrigin     = "https://www.alipan.com"
)

type client struct {
	httpClient *http.Client
	apiBaseURL string
	authURL    string

	mu           sync.RWMutex
	accessToken  string
	refreshToken string
	userID       string
	deviceID     string
	signature    string
	onRefresh    func(accessToken, refreshToken string)
}

type clientOptions struct {
	APIBaseURL string
	AuthURL    string
	HTTPClient *http.Client
}

func newClient(refreshToken string, opts clientOptions) *client {
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	authURL := opts.AuthURL
	if authURL == "" {
		authURL = defaultAuthURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = httputil.NewClient(60*time.Second, 30*time.Second)
	}
	return &client{
		httpClient:   httpClient,
		apiBaseURL:   apiBaseURL,
		authURL:      authURL,
		refreshToken: refreshToken,
	}
}

func (c *client) tokens() (accessToken, refreshToken string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken, c.refreshToken
}

func (c *client) refresh(ctx context.Context) error {
	_, refreshToken := c.tokens()
	if refreshToken == "" {
		return fmt.Errorf("aliyundrive: refresh token is required")
	}
	body := map[string]string{
		"refresh_token": refreshToken,
		"grant_type":    "refresh_token",
	}
	var resp tokenResp
	if err := c.rawJSON(ctx, http.MethodPost, c.authURL, "", body, &resp); err != nil {
		return fmt.Errorf("aliyundrive: refresh token: %w", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		return fmt.Errorf("aliyundrive: refresh token returned empty token")
	}
	c.mu.Lock()
	c.accessToken = resp.AccessToken
	c.refreshToken = resp.RefreshToken
	c.mu.Unlock()
	if c.onRefresh != nil {
		c.onRefresh(resp.AccessToken, resp.RefreshToken)
	}
	return nil
}

func (c *client) setTokens(accessToken, refreshToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if accessToken != "" {
		c.accessToken = accessToken
	}
	if refreshToken != "" {
		c.refreshToken = refreshToken
	}
}

func (c *client) request(ctx context.Context, method, path string, body, result any) error {
	url := path
	if strings.HasPrefix(path, "/") {
		url = c.apiBaseURL + path
	}
	err := c.rawJSON(ctx, method, url, c.currentAccessToken(), body, result)
	var apiErr *apiStatusError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.code {
	case "AccessTokenInvalid":
		if refreshErr := c.refresh(ctx); refreshErr != nil {
			return refreshErr
		}
		return c.rawJSON(ctx, method, url, c.currentAccessToken(), body, result)
	case "DeviceSessionSignatureInvalid":
		if sessionErr := c.createSession(ctx); sessionErr != nil {
			return sessionErr
		}
		return c.rawJSON(ctx, method, url, c.currentAccessToken(), body, result)
	}
	return err
}

func (c *client) currentAccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken
}

func (c *client) currentDeviceHeaders() (deviceID, signature string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deviceID, c.signature
}

func (c *client) createSession(ctx context.Context) error {
	body, err := c.createSessionBody()
	if err != nil {
		return err
	}
	if err := c.rawJSON(ctx, http.MethodPost, c.apiBaseURL+"/users/v1/users/device/create_session", c.currentAccessToken(), body, nil); err != nil {
		return fmt.Errorf("aliyundrive: create device session: %w", err)
	}
	return nil
}

type apiStatusError struct {
	status  int
	code    string
	message string
}

func (e *apiStatusError) Error() string {
	if e.code != "" || e.message != "" {
		return fmt.Sprintf("aliyundrive: api error status=%d code=%s message=%s", e.status, e.code, e.message)
	}
	return fmt.Sprintf("aliyundrive: api error status=%d", e.status)
}

func (c *client) rawJSON(ctx context.Context, method, url, accessToken string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", defaultOrigin)
	req.Header.Set("Referer", defaultReferer)
	req.Header.Set("X-Canary", "client=Android,app=adrive,version=v4.1.0")
	req.Header.Set("x-request-id", requestID())
	deviceID, signature := c.currentDeviceHeaders()
	if deviceID != "" {
		req.Header.Set("X-Device-Id", deviceID)
	}
	if signature != "" {
		req.Header.Set("X-Signature", signature)
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer\t"+accessToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var apiErr apiError
	_ = json.Unmarshal(respBody, &apiErr)
	if resp.StatusCode >= 400 || apiErr.Code != "" {
		return &apiStatusError{status: resp.StatusCode, code: apiErr.Code, message: apiErr.Message}
	}
	if result == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("aliyundrive: decode response: %w", err)
	}
	return nil
}

func requestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
