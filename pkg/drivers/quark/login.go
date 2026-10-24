package quark

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	defaultLoginUOPBaseURL   = "https://uop.quark.cn"
	defaultLoginPanBaseURL   = "https://pan.quark.cn"
	defaultLoginQRBaseURL    = "https://su.quark.cn/4_eMHBJ"
	defaultLoginPollInterval = 2 * time.Second
	defaultLoginMaxRenewals  = 3
	loginQRClientID          = "532"
	loginQRVersion           = "1.2"
	loginQRBizStr            = "S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0"
	loginUserAgent           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	qrStatusOK        = 2000000
	qrStatusWaiting   = 50004001
	qrStatusExpired   = 50004002
	qrStatusFailed    = 50004003
	qrStatusCancelled = 50004004
)

var (
	errQRExpired       = errors.New("quark login: QR code expired")
	errLoginCancelled  = errors.New("quark login: cancelled")
	errLoginTimeout    = errors.New("quark login: timed out waiting for scan")
	errLoginUIRequired = errors.New("quark login: login UI is required")
)

// LoginOptions configures the interactive Quark login flow. Zero values use
// production defaults; tests override the base URLs and poll interval.
type LoginOptions struct {
	UOPBaseURL string
	PanBaseURL string
	// DriverBaseURL/DriverV2URL point the verification request at a custom
	// Quark API host. Empty uses the driver defaults.
	DriverBaseURL string
	DriverV2URL   string
	PollInterval  time.Duration
	MaxRenewals   int
	HTTPClient    *http.Client
	ChallengeURL  string
}

type loginProvider struct{}

func (loginProvider) Login(ctx context.Context, ui drive.LoginUI) (drive.LoginResult, error) {
	return loginFlow(ctx, LoginOptions{}, ui)
}

func (loginProvider) Account(params drive.Params) (string, bool) {
	account := cookieAccount(params["cookie"])
	return account, account != ""
}

func init() {
	drive.RegisterLoginProvider("quark", loginProvider{})
}

func loginFlow(ctx context.Context, opts LoginOptions, ui drive.LoginUI) (drive.LoginResult, error) {
	if ui == nil {
		return drive.LoginResult{}, errLoginUIRequired
	}
	opts = normalizeLoginOptions(opts)
	client := &loginClient{opts: opts}

	token, err := client.fetchQRToken(ctx)
	if err != nil {
		return drive.LoginResult{}, loginContextError(err)
	}
	renewals := 0
	for {
		if err := ctx.Err(); err != nil {
			return drive.LoginResult{}, loginContextError(err)
		}
		challenge := buildQRURL(opts, token)
		if err := ui.ShowChallenge(challenge); err != nil {
			return drive.LoginResult{}, redactLoginError(fmt.Errorf("show login challenge: %w", err), challenge, token)
		}
		ticket, err := client.pollServiceTicket(ctx, token)
		if errors.Is(err, errQRExpired) {
			if renewals >= opts.MaxRenewals {
				return drive.LoginResult{}, fmt.Errorf("quark login: QR code expired again after %d renewals", renewals)
			}
			renewals++
			token, err = client.fetchQRToken(ctx)
			if err != nil {
				return drive.LoginResult{}, loginContextError(err)
			}
			continue
		}
		if err != nil {
			return drive.LoginResult{}, err
		}
		cookie, err := client.exchangeServiceTicket(ctx, ticket)
		if err != nil {
			return drive.LoginResult{}, loginContextError(err)
		}
		if err := verifyLoginCookie(ctx, cookie, opts); err != nil {
			return drive.LoginResult{}, fmt.Errorf("quark login: verify cookie: %w", err)
		}
		account := cookieAccount(cookie)
		return drive.LoginResult{
			Params:  drive.Params{"cookie": cookie},
			Account: account,
		}, nil
	}
}

func normalizeLoginOptions(opts LoginOptions) LoginOptions {
	if strings.TrimSpace(opts.UOPBaseURL) == "" {
		opts.UOPBaseURL = defaultLoginUOPBaseURL
	}
	if strings.TrimSpace(opts.PanBaseURL) == "" {
		opts.PanBaseURL = defaultLoginPanBaseURL
	}
	if strings.TrimSpace(opts.ChallengeURL) == "" {
		opts.ChallengeURL = defaultLoginQRBaseURL
	}
	opts.UOPBaseURL = strings.TrimRight(opts.UOPBaseURL, "/")
	opts.PanBaseURL = strings.TrimRight(opts.PanBaseURL, "/")
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultLoginPollInterval
	}
	if opts.MaxRenewals == 0 {
		opts.MaxRenewals = defaultLoginMaxRenewals
	}
	if opts.MaxRenewals < 0 {
		opts.MaxRenewals = 0
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return opts
}

type loginClient struct {
	opts LoginOptions
}

type qrResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Members struct {
			Token         string `json:"token"`
			ServiceTicket string `json:"service_ticket"`
		} `json:"members"`
	} `json:"data"`
}

func (c *loginClient) fetchQRToken(ctx context.Context) (string, error) {
	requestID, err := newLoginRequestID()
	if err != nil {
		return "", fmt.Errorf("quark login: generate request id: %w", err)
	}
	query := url.Values{}
	query.Set("client_id", loginQRClientID)
	query.Set("v", loginQRVersion)
	query.Set("request_id", requestID)
	resp, err := c.getJSON(ctx, c.opts.UOPBaseURL+"/cas/ajax/getTokenForQrcodeLogin?"+query.Encode())
	if err != nil {
		return "", err
	}
	if resp.Status != qrStatusOK {
		return "", fmt.Errorf("quark login: request QR token failed: status %d", resp.Status)
	}
	token := resp.Data.Members.Token
	if token == "" {
		return "", errors.New("quark login: provider returned an empty QR token")
	}
	return token, nil
}

func (c *loginClient) pollServiceTicket(ctx context.Context, token string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", loginContextError(err)
		}
		requestID, err := newLoginRequestID()
		if err != nil {
			return "", fmt.Errorf("quark login: generate request id: %w", err)
		}
		query := url.Values{}
		query.Set("client_id", loginQRClientID)
		query.Set("v", loginQRVersion)
		query.Set("token", token)
		query.Set("request_id", requestID)
		resp, err := c.getJSON(ctx, c.opts.UOPBaseURL+"/cas/ajax/getServiceTicketByQrcodeToken?"+query.Encode())
		if err != nil {
			if ctx.Err() != nil {
				return "", loginContextError(ctx.Err())
			}
			return "", err
		}
		switch resp.Status {
		case qrStatusOK:
			ticket := resp.Data.Members.ServiceTicket
			if ticket == "" {
				return "", errors.New("quark login: provider returned an empty service ticket")
			}
			return ticket, nil
		case qrStatusWaiting:
			// Keep polling.
		case qrStatusExpired:
			return "", errQRExpired
		case qrStatusCancelled:
			return "", errLoginCancelled
		case qrStatusFailed:
			return "", errors.New("quark login: provider reported a login failure")
		default:
			return "", fmt.Errorf("quark login: unexpected status %d", resp.Status)
		}

		timer := time.NewTimer(c.opts.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", loginContextError(ctx.Err())
		case <-timer.C:
		}
	}
}

func (c *loginClient) exchangeServiceTicket(ctx context.Context, ticket string) (string, error) {
	jar := newRecordingJar()
	client := &http.Client{
		Transport: c.opts.HTTPClient.Transport,
		Timeout:   c.opts.HTTPClient.Timeout,
		Jar:       jar,
	}
	if c.opts.HTTPClient.CheckRedirect != nil {
		client.CheckRedirect = c.opts.HTTPClient.CheckRedirect
	}
	query := url.Values{}
	query.Set("st", ticket)
	query.Set("lw", "scan")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.PanBaseURL+"/account/info?"+query.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("quark login: create ticket request: %w", err)
	}
	applyLoginHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", sanitizeLoginError(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("quark login: ticket exchange returned HTTP %d", resp.StatusCode)
	}
	cookie := jar.header()
	if cookie == "" {
		return "", errors.New("quark login: provider returned no quark cookies")
	}
	return cookie, nil
}

func (c *loginClient) getJSON(ctx context.Context, endpoint string) (*qrResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("quark login: create request: %w", err)
	}
	applyLoginHeaders(req)
	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, sanitizeLoginError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("quark login: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("quark login: provider returned HTTP %d", resp.StatusCode)
	}
	var parsed qrResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("quark login: decode response: %w", err)
	}
	return &parsed, nil
}

func applyLoginHeaders(req *http.Request) {
	req.Header.Set("User-Agent", loginUserAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
}

func buildQRURL(opts LoginOptions, token string) string {
	query := url.Values{}
	query.Set("token", token)
	query.Set("client_id", loginQRClientID)
	query.Set("ssb", "weblogin")
	query.Set("uc_param_str", "")
	query.Set("uc_biz_str", loginQRBizStr)
	return opts.ChallengeURL + "?" + query.Encode()
}

func newLoginRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func verifyLoginCookie(ctx context.Context, cookie string, opts LoginOptions) error {
	driver := New(cookie, Options{
		BaseURL: opts.DriverBaseURL,
		V2URL:   opts.DriverV2URL,
	})
	defer func() {
		_ = driver.Drop(ctx)
	}()
	return driver.Init(ctx)
}

func loginContextError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errLoginTimeout
	case errors.Is(err, context.Canceled):
		return errLoginCancelled
	default:
		return err
	}
}

// sanitizeLoginError removes the request URL from net/http errors: challenge
// and ticket URLs carry tokens that must never reach logs or user output.
func sanitizeLoginError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return redactLoginError(err)
}

var loginSecretQueryPattern = regexp.MustCompile(`(?i)([?&](?:token|st|service_ticket)=)[^&\s]+`)

type redactedLoginError struct {
	err     error
	secrets []string
}

func (e redactedLoginError) Error() string {
	text := e.err.Error()
	for _, secret := range e.secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	return loginSecretQueryPattern.ReplaceAllString(text, "${1}<redacted>")
}

func (e redactedLoginError) Unwrap() error { return e.err }

// redactLoginError keeps error identity while removing known challenge material
// (full URLs, tokens) and any token/st/service_ticket query value from text.
func redactLoginError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return redactedLoginError{err: err, secrets: secrets}
}

type recordingJar struct {
	base   http.CookieJar
	mu     sync.Mutex
	values map[string]string
}

func newRecordingJar() *recordingJar {
	base, _ := cookiejar.New(nil)
	return &recordingJar{base: base, values: map[string]string{}}
}

func (j *recordingJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.base.SetCookies(u, cookies)
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if domain == "" {
			domain = strings.ToLower(u.Hostname())
		}
		if !isQuarkCookieDomain(domain) {
			continue
		}
		j.values[cookie.Name] = cookie.Value
	}
}

func (j *recordingJar) Cookies(u *url.URL) []*http.Cookie {
	return j.base.Cookies(u)
}

func (j *recordingJar) header() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	names := make([]string, 0, len(j.values))
	for name := range j.values {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+j.values[name])
	}
	return strings.Join(pairs, "; ")
}

func isQuarkCookieDomain(domain string) bool {
	domain = strings.TrimPrefix(strings.ToLower(domain), ".")
	return domain == "quark.cn" || strings.HasSuffix(domain, ".quark.cn")
}
