package p189

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	apiBase         = "https://cloud.189.cn"
	uploadBase      = "https://upload.cloud.189.cn"
	loginURL        = apiBase + "/api/portal/loginUrl.action?redirectURL=https%3A%2F%2Fcloud.189.cn%2Fmain.action"
	listURL         = apiBase + "/api/open/file/listFiles.action"
	downloadInfoURL = apiBase + "/api/portal/getFileInfo.action"
	mkdirURL        = apiBase + "/api/open/file/createFolder.action"
	renameFileURL   = apiBase + "/api/open/file/renameFile.action"
	renameFolderURL = apiBase + "/api/open/file/renameFolder.action"
	batchTaskURL    = apiBase + "/api/open/batch/createBatchTask.action"
	uploadInitURL   = uploadBase + "/person/initMultiUpload"
	uploadDataURL   = uploadBase + "/person/uploadData"
	uploadCommitURL = uploadBase + "/person/commitMultiUpload"
	spaceURL        = apiBase + "/api/portal/getUserSizeInfo.action"
)

type client struct {
	hc         *http.Client
	cookie     string
	username   string
	password   string
	sessionKey string
	pubKey     string
	pkID       string
	keyExpire  int64
	traceMu    sync.Mutex
	trace      []drive.DebugTraceEvent
}

type p189TraceRequestFieldsKey struct{}

var sensitiveSnippetPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(sessionKey=)[^,\s"']+`),
	regexp.MustCompile(`(?i)("sessionKey"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("downloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("fileDownloadUrl"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)("requestURL"\s*:\s*")[^"]+`),
	regexp.MustCompile(`(?i)(Cookie:\s*)[^,\s"']+`),
	regexp.MustCompile(`(?i)(token=)[^&\s"']+`),
}

func newClient(cookie, username, password string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{
		hc:       &http.Client{Jar: jar},
		cookie:   cookie,
		username: username,
		password: password,
	}
}

func (c *client) recordTrace(ctx context.Context, event drive.DebugTraceEvent) {
	if op, ok := drive.DebugOperationFromContext(ctx); ok {
		event.OpID = op.OpID
		event.Step = op.Step
		event.Name = op.Name
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	if event.Layer == "" {
		event.Layer = "driver.http"
	}
	event.SensitiveMasked = true
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	c.trace = append(c.trace, event)
	if len(c.trace) > 500 {
		c.trace = append([]drive.DebugTraceEvent(nil), c.trace[len(c.trace)-500:]...)
	}
}

func (c *client) debugTrace(since time.Time) []drive.DebugTraceEvent {
	c.traceMu.Lock()
	defer c.traceMu.Unlock()
	events := make([]drive.DebugTraceEvent, 0, len(c.trace))
	for _, event := range c.trace {
		if event.At.Before(since) {
			continue
		}
		events = append(events, event)
	}
	return events
}

func traceRequestFields(fields map[string]string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]any{"fields": keys}
}

func traceURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	out := *u
	out.RawQuery = ""
	out.ForceQuery = false
	out.RawFragment = ""
	out.Fragment = ""
	out.User = nil
	return out.String()
}

func (c *client) isLoggedIn() bool {
	if c.cookie != "" {
		return true
	}
	return c.username != "" && c.password != ""
}

func (c *client) loginInit(ctx context.Context) error {
	if c.cookie != "" {
		return c.loginWithCookie(ctx)
	}
	if c.username != "" {
		return c.loginWithPassword(ctx)
	}
	return fmt.Errorf("189: no credentials")
}

func (c *client) loginWithCookie(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Cookie", c.cookie)
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: "login_cookie",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Error:     err.Error(),
		})
		return err
	}
	resp.Body.Close()
	c.recordTrace(ctx, drive.DebugTraceEvent{
		Operation: "login_cookie",
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
	})
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("189: cookie login: %s", resp.Status)
	}
	c.extractSessionKey()
	return nil
}

func (c *client) loginWithPassword(ctx context.Context) error {
	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	req1.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req1.Header.Set("Referer", "https://cloud.189.cn/")
	resp1, err := c.hc.Do(req1)
	if err != nil {
		return err
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		return fmt.Errorf("189: login page: %s", resp1.Status)
	}

	redirectURL := resp1.Request.URL.String()
	if redirectURL == "https://cloud.189.cn/web/main" {
		return nil
	}
	lt := resp1.Request.URL.Query().Get("lt")
	reqID := resp1.Request.URL.Query().Get("reqId")
	appID := resp1.Request.URL.Query().Get("appId")

	loginHeaders := map[string]string{
		"lt":      lt,
		"reqid":   reqID,
		"referer": redirectURL,
		"origin":  "https://open.e.189.cn",
	}

	var appConf struct {
		Result string `json:"result"`
		Msg    string `json:"msg"`
		Data   struct {
			AccountType string `json:"accountType"`
			AppKey      string `json:"appKey"`
			ClientType  int    `json:"clientType"`
			IsOauth2    bool   `json:"isOauth2"`
			ReturnUrl   string `json:"returnUrl"`
			MailSuffix  string `json:"mailSuffix"`
			ParamId     string `json:"paramId"`
		} `json:"data"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/oauth2/appConf.do", loginHeaders, map[string]string{
		"version": "2.0",
		"appKey":  appID,
	}, &appConf)
	if err != nil {
		return fmt.Errorf("189: app conf: %w", err)
	}
	if appConf.Result != "0" {
		return fmt.Errorf("189: app conf: %s", appConf.Msg)
	}

	var encConf struct {
		Result int `json:"result"`
		Data   struct {
			Pre       string `json:"pre"`
			PreDomain string `json:"preDomain"`
			PubKey    string `json:"pubKey"`
		} `json:"data"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/config/encryptConf.do", loginHeaders, map[string]string{
		"appId": appID,
	}, &encConf)
	if err != nil {
		return fmt.Errorf("189: encrypt conf: %w", err)
	}
	if encConf.Result != 0 {
		return fmt.Errorf("189: encrypt conf failed")
	}

	encUser := encConf.Data.Pre + rsaEncode([]byte(c.username), encConf.Data.PubKey, true)
	encPass := encConf.Data.Pre + rsaEncode([]byte(c.password), encConf.Data.PubKey, true)

	var loginResp struct {
		Result int    `json:"result"`
		Msg    string `json:"msg"`
		ToURL  string `json:"toUrl"`
	}
	err = c.doPostWithHeaders(ctx, "https://open.e.189.cn/api/logbox/oauth2/loginSubmit.do", loginHeaders, map[string]string{
		"version":         "v2.0",
		"apToken":         "",
		"appKey":          appID,
		"accountType":     appConf.Data.AccountType,
		"userName":        encUser,
		"epd":             encPass,
		"captchaType":     "",
		"validateCode":    "",
		"smsValidateCode": "",
		"captchaToken":    "",
		"returnUrl":       appConf.Data.ReturnUrl,
		"mailSuffix":      appConf.Data.MailSuffix,
		"dynamicCheck":    "FALSE",
		"clientType":      strconv.Itoa(appConf.Data.ClientType),
		"cb_SaveName":     "3",
		"isOauth2":        strconv.FormatBool(appConf.Data.IsOauth2),
		"state":           "",
		"paramId":         appConf.Data.ParamId,
	}, &loginResp)
	if err != nil {
		return fmt.Errorf("189: login submit: %w", err)
	}
	if loginResp.Result != 0 {
		return fmt.Errorf("189: login failed: %s", loginResp.Msg)
	}

	nextURL := loginResp.ToURL
	if nextURL == "" {
		nextURL = "https://cloud.189.cn/web/main"
	}
	mainReq, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
	if err != nil {
		return err
	}
	mainReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	mainReq.Header.Set("Referer", "https://cloud.189.cn/")
	mainResp, err := c.hc.Do(mainReq)
	if err != nil {
		return fmt.Errorf("189: main page: %w", err)
	}
	mainResp.Body.Close()
	return nil
}

func (c *client) getSessionKey(ctx context.Context) error {
	var resp struct {
		SessionKey string `json:"sessionKey"`
	}
	err := c.doGet(ctx, "https://cloud.189.cn/v2/getUserBriefInfo.action", nil, &resp)
	if err != nil {
		return err
	}
	if resp.SessionKey == "" {
		return fmt.Errorf("189: empty session key")
	}
	c.sessionKey = resp.SessionKey
	return nil
}

func (c *client) extractSessionKey() {
	u, _ := url.Parse("https://cloud.189.cn")
	if u == nil {
		return
	}
	for _, cookie := range c.hc.Jar.Cookies(u) {
		if cookie.Name == "COOKIE_LOGIN_SESSION" || cookie.Name == "JSESSIONID" || cookie.Name == "SESSION" {
			c.sessionKey = cookie.Value
			return
		}
	}
}

func (c *client) getResKey(ctx context.Context) (string, string, error) {
	now := time.Now().UnixMilli()
	if c.keyExpire > now && c.pubKey != "" && c.pkID != "" {
		return c.pubKey, c.pkID, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloud.189.cn/api/security/generateRsaKey.action", nil)
	if err != nil {
		return "", "", err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("189: get res key: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", "", fmt.Errorf("189: empty res key response")
	}
	if trimmed[0] == '<' {
		var xmlResp struct {
			XMLName xml.Name `xml:"keyPair"`
			Result  int      `xml:"result"`
			PubKey  string   `xml:"pubKey"`
			PkID    string   `xml:"pkId"`
			Expire  int64    `xml:"expire"`
		}
		if err := xml.Unmarshal(trimmed, &xmlResp); err != nil {
			return "", "", fmt.Errorf("189: res key xml decode: %w", err)
		}
		if xmlResp.Result != 0 {
			return "", "", fmt.Errorf("189: res key failed")
		}
		c.pubKey = xmlResp.PubKey
		c.pkID = xmlResp.PkID
		c.keyExpire = xmlResp.Expire
	} else {
		var jsonResp struct {
			Result int    `json:"result"`
			PubKey string `json:"pubKey"`
			PkID   string `json:"pkId"`
			Expire int64  `json:"expire"`
		}
		if err := json.Unmarshal(trimmed, &jsonResp); err != nil {
			return "", "", fmt.Errorf("189: res key json decode: %w", err)
		}
		c.pubKey = jsonResp.PubKey
		c.pkID = jsonResp.PkID
		c.keyExpire = jsonResp.Expire
	}
	return c.pubKey, c.pkID, nil
}

func (c *client) uploadRequest(ctx context.Context, uri string, form map[string]string) ([]byte, error) {
	if c.sessionKey == "" {
		if err := c.getSessionKey(ctx); err != nil {
			return nil, fmt.Errorf("189: get session key: %w", err)
		}
	}
	pubKey, pkID, err := c.getResKey(ctx)
	if err != nil {
		return nil, err
	}
	headers, params, err := generateUploadHeaders(c.sessionKey, uri, form, pubKey, pkID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://upload.cloud.189.cn"+uri+"?params="+params, nil)
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: uri,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: uri,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return nil, fmt.Errorf("189: upload request %s: %s body=%q", uri, resp.Status, responseSnippet(raw))
	}
	raw, err := io.ReadAll(resp.Body)
	event := drive.DebugTraceEvent{
		Operation: uri,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   traceRequestFields(form),
		Response:  map[string]any{"bytes": len(raw)},
	}
	if err != nil {
		event.Error = err.Error()
	}
	c.recordTrace(ctx, event)
	return raw, err
}

func (c *client) doPostRaw(ctx context.Context, u string, form map[string]string) ([]byte, error) {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return nil, fmt.Errorf("189: %s %s: %s body=%q", req.Method, req.URL, resp.Status, responseSnippet(raw))
	}
	raw, err := io.ReadAll(resp.Body)
	event := drive.DebugTraceEvent{
		Operation: req.URL.Path,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   traceRequestFields(form),
		Response:  map[string]any{"bytes": len(raw)},
	}
	if err != nil {
		event.Error = err.Error()
	}
	c.recordTrace(ctx, event)
	return raw, err
}

func responseSnippet(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 300 {
		raw = raw[:300]
	}
	snippet := string(raw)
	for _, pattern := range sensitiveSnippetPatterns {
		snippet = pattern.ReplaceAllString(snippet, "${1}<masked>")
	}
	return snippet
}

func (c *client) doGetRaw(ctx context.Context, u string, query map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Accept", "application/json;charset=UTF-8")
	q := req.URL.Query()
	q.Set("noCache", noCache())
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(query),
			Error:     err.Error(),
		})
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(query),
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return nil, fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	event := drive.DebugTraceEvent{
		Operation: req.URL.Path,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   traceRequestFields(query),
		Response:  map[string]any{"bytes": len(raw)},
	}
	if err != nil {
		event.Error = err.Error()
	}
	c.recordTrace(ctx, event)
	return raw, err
}

func (c *client) doPostWithHeaders(ctx context.Context, u string, headers map[string]string, form map[string]string, result any) error {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if lt, ok := headers["lt"]; ok {
		req.Header.Set("lt", lt)
	}
	if reqid, ok := headers["reqid"]; ok {
		req.Header.Set("reqid", reqid)
	}
	if referer, ok := headers["referer"]; ok {
		req.Header.Set("Referer", referer)
	}
	if origin, ok := headers["origin"]; ok {
		req.Header.Set("Origin", origin)
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return fmt.Errorf("189: %s: %s", u, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return err
	}
	c.recordTrace(ctx, drive.DebugTraceEvent{
		Operation: req.URL.Path,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   traceRequestFields(form),
		Response:  map[string]any{"bytes": len(raw)},
	})
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("189: decode: %w", err)
		}
	}
	return nil
}

func (c *client) doGet(ctx context.Context, u string, query map[string]string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	q := req.URL.Query()
	q.Set("noCache", noCache())
	for k, v := range query {
		q.Set(k, v)
	}
	req.URL.RawQuery = q.Encode()
	req = req.WithContext(context.WithValue(req.Context(), p189TraceRequestFieldsKey{}, query))
	return c.doReq(req, result)
}

func (c *client) doPost(ctx context.Context, u string, form map[string]string, result any) error {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	q := req.URL.Query()
	q.Set("noCache", noCache())
	req.URL.RawQuery = q.Encode()
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   traceRequestFields(form),
			Error:     err.Error(),
		})
		return fmt.Errorf("189: read response: %w", err)
	}
	c.recordTrace(ctx, drive.DebugTraceEvent{
		Operation: req.URL.Path,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   traceRequestFields(form),
		Response:  map[string]any{"bytes": len(raw)},
	})
	if len(raw) == 0 || result == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if trimmed[0] == '<' {
		return xml.Unmarshal(trimmed, result)
	}
	return json.Unmarshal(trimmed, result)
}

func (c *client) doReq(req *http.Request, result any) error {
	start := time.Now()
	var request map[string]any
	if fields, ok := req.Context().Value(p189TraceRequestFieldsKey{}).(map[string]string); ok {
		request = traceRequestFields(fields)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.recordTrace(req.Context(), drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   request,
			Error:     err.Error(),
		})
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		c.recordTrace(req.Context(), drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   request,
			Response:  map[string]any{"body_snippet": responseSnippet(raw)},
		})
		return fmt.Errorf("189: %s %s: %s", req.Method, req.URL, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.recordTrace(req.Context(), drive.DebugTraceEvent{
			Operation: req.URL.Path,
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   request,
			Error:     err.Error(),
		})
		return fmt.Errorf("189: read response: %w", err)
	}
	c.recordTrace(req.Context(), drive.DebugTraceEvent{
		Operation: req.URL.Path,
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   request,
		Response:  map[string]any{"bytes": len(raw)},
	})
	if len(raw) == 0 {
		return nil
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("189: decode response from %s: %w (body: %s)", req.URL, err, string(raw[:min(len(raw), 200)]))
		}
	}
	return nil
}

func (c *client) retryOnAuthError(ctx context.Context, fn func(context.Context) error) error {
	err := fn(ctx)
	if err != nil && c.username != "" && isAuthError(err) {
		c.cookie = ""
		if loginErr := c.loginWithPassword(ctx); loginErr == nil {
			if keyErr := c.getSessionKey(ctx); keyErr == nil {
				err = fn(ctx)
			}
		}
	}
	return err
}

func isAuthError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "400") || strings.Contains(s, "InvalidSessionKey") || strings.Contains(s, "401") || strings.Contains(s, "403")
}

func (c *client) listFiles(ctx context.Context, folderID int64) (folders []Folder, files []File, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		folders = nil
		files = nil
		for pageNum := 1; ; pageNum++ {
			body, e := c.doGetRaw(ctx, listURL, map[string]string{
				"pageSize":   "60",
				"pageNum":    strconv.Itoa(pageNum),
				"mediaType":  "0",
				"folderId":   strconv.FormatInt(folderID, 10),
				"iconOption": "5",
				"orderBy":    "lastOpTime",
				"descending": "true",
			})
			if e != nil {
				return e
			}
			pageFolders, pageFiles, count, e := parseListFilesBody(body)
			if e != nil {
				return e
			}
			if count == 0 && len(pageFolders) == 0 && len(pageFiles) == 0 {
				break
			}
			folders = append(folders, pageFolders...)
			files = append(files, pageFiles...)
			if count < 60 {
				break
			}
		}
		return nil
	})
	return
}

func parseListFilesBody(body []byte) ([]Folder, []File, int, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, nil, 0, nil
	}
	if trimmed[0] == '<' {
		var xmlResp xmlListFiles
		if err := xml.Unmarshal(trimmed, &xmlResp); err != nil {
			return nil, nil, 0, fmt.Errorf("189: list xml decode: %w", err)
		}
		folders := make([]Folder, 0, len(xmlResp.FileList.Folders))
		for _, f := range xmlResp.FileList.Folders {
			folders = append(folders, Folder{
				ID:         f.ID,
				Name:       f.Name,
				LastOpTime: f.CreateDate,
			})
		}
		files := make([]File, 0, len(xmlResp.FileList.Files))
		for _, f := range xmlResp.FileList.Files {
			files = append(files, File{
				ID:         f.ID,
				Name:       f.Name,
				Size:       f.Size,
				LastOpTime: f.LastOpTime,
			})
		}
		return folders, files, len(folders) + len(files), nil
	}
	var resp ListResp
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, nil, 0, fmt.Errorf("189: list json decode: %w", err)
	}
	if resp.ResCode != 0 {
		return nil, nil, 0, fmt.Errorf("189: list: %s", resp.ResMessage)
	}
	return resp.FileListAO.FolderList, resp.FileListAO.FileList, resp.FileListAO.Count, nil
}

func (c *client) withRetry(ctx context.Context, fn func(context.Context) error) error {
	return c.retryOnAuthError(ctx, fn)
}

func (c *client) getDownloadURL(ctx context.Context, fileID int64) (downloadURL string, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		var resp DownResp
		if e := c.doGet(ctx, downloadInfoURL, map[string]string{
			"fileId": strconv.FormatInt(fileID, 10),
		}, &resp); e != nil {
			return e
		}
		if resp.ResCode != 0 {
			return fmt.Errorf("189: get download url: %s", resp.ResMessage)
		}
		downloadURL = resp.FileDownloadURL
		return nil
	})
	return
}

func (c *client) createFolder(ctx context.Context, parentID int64, name string) (id int64, err error) {
	err = c.retryOnAuthError(ctx, func(ctx context.Context) error {
		var resp MkdirResp
		if e := c.doPost(ctx, mkdirURL, map[string]string{
			"parentFolderId": strconv.FormatInt(parentID, 10),
			"folderName":     name,
		}, &resp); e != nil {
			return e
		}
		id = resp.ID
		return nil
	})
	return
}

func (c *client) rename(ctx context.Context, fileID int64, name string, isDir bool) error {
	return c.retryOnAuthError(ctx, func(ctx context.Context) error {
		u := renameFileURL
		idKey := "fileId"
		nameKey := "destFileName"
		if isDir {
			u = renameFolderURL
			idKey = "folderId"
			nameKey = "destFolderName"
		}
		return c.doPost(ctx, u, map[string]string{
			idKey:   strconv.FormatInt(fileID, 10),
			nameKey: name,
		}, nil)
	})
}

func (c *client) batchTask(ctx context.Context, taskType string, taskInfos string, targetFolderID string) error {
	return c.retryOnAuthError(ctx, func(ctx context.Context) error {
		form := map[string]string{
			"type":      taskType,
			"taskInfos": taskInfos,
		}
		if targetFolderID != "" {
			form["targetFolderId"] = targetFolderID
		}
		return c.doPost(ctx, batchTaskURL, form, nil)
	})
}

func (c *client) initUpload(ctx context.Context, parentID int64, name string, size int64, fileMd5, sliceMd5 string) (string, bool, error) {
	raw, err := c.uploadRequest(ctx, "/person/initMultiUpload", map[string]string{
		"parentFolderId": strconv.FormatInt(parentID, 10),
		"fileName":       name,
		"fileSize":       strconv.FormatInt(size, 10),
		"sliceSize":      strconv.FormatInt(uploadPartSize, 10),
		"fileMd5":        fileMd5,
		"sliceMd5":       sliceMd5,
	})
	if err != nil {
		return "", false, err
	}
	var result struct {
		Code string `json:"code"`
		Data struct {
			UploadFileID   string `json:"uploadFileId"`
			FileDataExists int    `json:"fileDataExists"`
			FileData       string `json:"fileData"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", false, fmt.Errorf("189: init upload decode: %w", err)
	}
	if result.Code != "SUCCESS" {
		return "", false, fmt.Errorf("189: init upload: %s", result.Code)
	}
	return result.Data.UploadFileID, result.Data.FileDataExists == 1, nil
}

func (c *client) uploadData(ctx context.Context, uploadFileID string, partInfo string) (map[string]uploadPart, error) {
	raw, err := c.uploadRequest(ctx, "/person/getMultiUploadUrls", map[string]string{
		"uploadFileId": uploadFileID,
		"partInfo":     partInfo,
	})
	if err != nil {
		return nil, err
	}
	var resp UploadUrlsResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("189: upload data decode: %w", err)
	}
	if resp.Code != "SUCCESS" {
		return nil, fmt.Errorf("189: upload data: %s", resp.Code)
	}
	return resp.UploadUrls, nil
}

func (c *client) commitUpload(ctx context.Context, uploadFileID string, fileMd5 string, sliceMd5 string) error {
	_, err := c.uploadRequest(ctx, "/person/commitMultiUploadFile", map[string]string{
		"uploadFileId": uploadFileID,
		"fileMd5":      fileMd5,
		"sliceMd5":     sliceMd5,
		"lazyCheck":    "1",
		"opertype":     "3",
	})
	return err
}

func (c *client) getCapacity(ctx context.Context) (*CapacityResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spaceURL, nil)
	if err != nil {
		return nil, err
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://cloud.189.cn/")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("189: space: %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("189: read space: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("189: empty space response")
	}
	if trimmed[0] == '<' {
		var xmlCap xmlCapacity
		if err := xml.Unmarshal(trimmed, &xmlCap); err != nil {
			return nil, fmt.Errorf("189: space xml decode: %w", err)
		}
		return &CapacityResp{
			CloudCapacityInfo: struct {
				TotalSize int64 `json:"totalSize"`
				FreeSize  int64 `json:"freeSize"`
				UsedSize  int64 `json:"usedSize"`
			}{
				TotalSize: xmlCap.CloudCapacityInfo.TotalSize,
				FreeSize:  xmlCap.CloudCapacityInfo.FreeSize,
				UsedSize:  xmlCap.CloudCapacityInfo.UsedSize,
			},
		}, nil
	}
	var capResp CapacityResp
	if err := json.Unmarshal(trimmed, &capResp); err != nil {
		return nil, fmt.Errorf("189: space json decode: %w", err)
	}
	return &capResp, nil
}

func noCache() string {
	return strconv.FormatInt(rand.Int63n(100000000000000000), 10)
}
