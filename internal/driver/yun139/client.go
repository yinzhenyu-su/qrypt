package yun139

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/httputil"
)

const (
	defaultBaseURL = "https://yun.139.com"
	letterBytes    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	mcloudChannel  = "1000101"
	mcloudClient   = "10701"
	mcloudRoute    = "001"
	mcloudVersion  = "7.14.0"
	xYunAPIVersion = "v1"
	xYunAppChannel = "10000034"
	xYunChSource   = "10000034"
	xYunClientInfo = "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||"
	xYunModuleType = "100"
	xYunSvcType    = "1"

	httpMaxRetries    = 3
	personalRetryWait = 500 * time.Millisecond
	authRefreshSkew   = 15 * 24 * time.Hour
)

func randomString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func encodeURIComponent(s string) string {
	r := url.QueryEscape(s)
	r = strings.Replace(r, "+", "%20", -1)
	r = strings.Replace(r, "%21", "!", -1)
	r = strings.Replace(r, "%27", "'", -1)
	r = strings.Replace(r, "%28", "(", -1)
	r = strings.Replace(r, "%29", ")", -1)
	r = strings.Replace(r, "%2A", "*", -1)
	return r
}

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return fmt.Sprintf("%X", h[:])
}

// calSign computes the mcloud-sign header value.
func calSign(bodyJSON, ts, randStr string) string {
	encoded := encodeURIComponent(bodyJSON)
	strs := strings.Split(encoded, "")
	sort.Strings(strs)
	sorted := strings.Join(strs, "")
	b64 := base64.StdEncoding.EncodeToString([]byte(sorted))
	res := md5Hex(b64) + md5Hex(ts+":"+randStr)
	return strings.ToUpper(md5Hex(res))
}

type client struct {
	httpClient            *http.Client
	authorization         string
	account               string
	personalCloudHost     string
	authRefreshURL        string
	onAuthorizationUpdate func(authorization string)
	mu                    sync.RWMutex
	tokenExpiry           time.Time
}

func newClient(authorization string) *client {
	return &client{
		httpClient:     httputil.NewClient(60*time.Second, 30*time.Second),
		authorization:  authorization,
		authRefreshURL: "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do",
	}
}

func (c *client) getAuthorization() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authorization
}

func (c *client) getAccount() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.account
}

func (c *client) setAuthorization(auth string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authorization = auth
}

func (c *client) decodeAuth() (account, token string, err error) {
	account, token, _, err = c.decodeAuthWithExpiry()
	return account, token, err
}

func (c *client) decodeAuthWithExpiry() (account, token string, expiry time.Time, err error) {
	raw := c.getAuthorization()
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode auth: %w", err)
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) < 3 {
		return "", "", time.Time{}, fmt.Errorf("invalid auth format")
	}
	c.account = parts[1]
	tokenParts := strings.Split(parts[2], "|")
	if len(tokenParts) >= 4 {
		expiryMS, parseErr := strconv.ParseInt(tokenParts[3], 10, 64)
		if parseErr == nil && expiryMS > 0 {
			expiry = time.UnixMilli(expiryMS)
			c.tokenExpiry = expiry
		}
	}
	return parts[1], parts[2], expiry, nil
}

func (c *client) refreshTokenIfNeeded(ctx context.Context) error {
	_, _, expiry, err := c.decodeAuthWithExpiry()
	if err != nil {
		return err
	}
	if expiry.IsZero() {
		return nil
	}
	remaining := time.Until(expiry)
	if remaining < 0 {
		return fmt.Errorf("authorization has expired")
	}
	if remaining > authRefreshSkew {
		return nil
	}
	return c.refreshToken(ctx)
}

func (c *client) refreshToken(ctx context.Context) error {
	account, token, err := c.decodeAuth()
	if err != nil {
		return err
	}

	body := fmt.Sprintf("<root><token>%s</token><account>%s</account><clienttype>656</clienttype></root>", token, account)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authRefreshURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var refreshResp struct {
		Return string `xml:"return"`
		Desc   string `xml:"desc"`
		Token  string `xml:"token"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return fmt.Errorf("token refresh: %w", err)
	}
	if refreshResp.Return != "0" {
		return fmt.Errorf("token refresh failed: %s", refreshResp.Desc)
	}

	raw := c.authorization
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("token refresh decode auth: %w", err)
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) >= 3 {
		parts[2] = refreshResp.Token
	}
	newAuth := base64.StdEncoding.EncodeToString([]byte(strings.Join(parts, ":")))
	c.setAuthorization(newAuth)
	if c.onAuthorizationUpdate != nil {
		c.onAuthorizationUpdate(newAuth)
	}
	return nil
}

// ensurePersonalCloudHost discovers the user's personal cloud API host.
// Uses a hard 15-second timeout; never blocks on an unreachable host.
func (c *client) ensurePersonalCloudHost() error {
	c.mu.RLock()
	if c.personalCloudHost != "" {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.personalCloudHost != "" {
		return nil
	}
	if c.account == "" {
		if _, _, err := c.decodeAuth(); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	routeURL := "https://user-njs.yun.139.com/user/route/qryRoutePolicy"
	routeData := map[string]interface{}{
		"userInfo": map[string]interface{}{
			"userType":    1,
			"accountType": 1,
			"accountName": c.account,
		},
		"modAddrType": 1,
	}
	body, _ := json.Marshal(routeData)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routeURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+c.authorization)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var routeResp struct {
		Data struct {
			RoutePolicyList []struct {
				ModName  string `json:"modName"`
				HttpsUrl string `json:"httpsUrl"`
			} `json:"routePolicyList"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &routeResp); err != nil {
		return fmt.Errorf("route policy: %w", err)
	}
	for _, item := range routeResp.Data.RoutePolicyList {
		if item.ModName == "personal" && item.HttpsUrl != "" {
			c.personalCloudHost = strings.TrimRight(item.HttpsUrl, "/")
			return nil
		}
	}
	return fmt.Errorf("no personal cloud host in route policy")
}

// personalPost sends a signed POST to the personal cloud API.
func (c *client) personalPost(ctx context.Context, path string, bodyData interface{}, result interface{}) error {
	var lastErr error
	for attempt := 0; attempt <= httpMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(personalRetryWait << uint(attempt))
		}
		if err := c.personalPostOnce(ctx, path, bodyData, result); err != nil {
			if isAuthExpiredError(err) {
				if refreshErr := c.refreshToken(ctx); refreshErr != nil {
					lastErr = fmt.Errorf("%w; refresh failed: %v", err, refreshErr)
					continue
				}
				if retryErr := c.personalPostOnce(ctx, path, bodyData, result); retryErr == nil {
					return nil
				} else {
					lastErr = retryErr
					continue
				}
			}
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func isAuthExpiredError(err error) bool {
	if err == nil {
		return false
	}
	return isAuthExpiredMessage(err.Error())
}

func isAuthExpiredMessage(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, "auth expired") ||
		strings.Contains(msg, "authorization") && strings.Contains(msg, "expired") ||
		strings.Contains(msg, "token") && strings.Contains(msg, "expired")
}

func (c *client) personalPostOnce(ctx context.Context, path string, bodyData interface{}, result interface{}) error {
	if err := c.ensurePersonalCloudHost(); err != nil {
		return err
	}

	bodyStr := ""
	if bodyData != nil {
		data, _ := json.Marshal(bodyData)
		bodyStr = string(data)
	}

	ts := time.Now().Format("2006-01-02 15:04:05")
	randStr := randomString(16)
	sign := calSign(bodyStr, ts, randStr)

	var bodyReader io.Reader
	if bodyStr != "" {
		bodyReader = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.personalCloudHost+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", "Basic "+c.getAuthorization())
	req.Header.Set("Caller", "web")
	req.Header.Set("Cms-Device", "default")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcloud-Channel", mcloudChannel)
	req.Header.Set("Mcloud-Client", mcloudClient)
	req.Header.Set("Mcloud-Route", mcloudRoute)
	req.Header.Set("Mcloud-Sign", fmt.Sprintf("%s,%s,%s", ts, randStr, sign))
	req.Header.Set("Mcloud-Version", mcloudVersion)
	req.Header.Set("X-Yun-Api-Version", xYunAPIVersion)
	req.Header.Set("X-Yun-App-Channel", xYunAppChannel)
	req.Header.Set("X-Yun-Channel-Source", xYunChSource)
	req.Header.Set("X-Yun-Client-Info", xYunClientInfo)
	req.Header.Set("X-Yun-Module-Type", xYunModuleType)
	req.Header.Set("X-Yun-Svc-Type", xYunSvcType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(respBody) > 0 && respBody[0] == '<' {
		return fmt.Errorf("personal API returned non-JSON: %s", string(respBody))
	}
	var base baseResp
	if err := json.Unmarshal(respBody, &base); err == nil && !base.Success && isAuthExpiredMessage(base.Message) {
		return fmt.Errorf("%s", base.Message)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, result); err != nil {
		return fmt.Errorf("personal API: %w", err)
	}
	return nil
}
