// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-resty/resty/v2"
	"golang.org/x/time/rate"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

const defaultAppVer = "35.6.0.3"
const md5Salt = "Qclm8MGWUv59TnrR0XPg"

var appVer = defaultAppVer
var loginCheckRetryDelays = []time.Duration{1 * time.Second, 2 * time.Second}

type Driver struct {
	drive.UnsupportedOperations
	cl               *driver115.Pan115Client
	rootID           string
	rootPath         string
	cookies          string
	limitRate        float64
	limiter          *rate.Limiter
	bandwidthLimiter *drive.BandwidthLimiter
	httpClient       *http.Client
	metrics          *util.Buffer
	stateStore       drive.StateStore
	cookieSource     string
	cookieUpdated    time.Time
	debugMu          sync.Mutex
	lastError        string
	instantUploads   atomic.Int64
}

type cookieState struct {
	Cookie    string    `json:"cookie,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type p115UploadSessionState struct {
	Version  int                          `json:"version"`
	Sessions map[string]p115UploadSession `json:"sessions,omitempty"`
}

type p115UploadSession struct {
	Key       string    `json:"key"`
	ParentID  string    `json:"parent_id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA1      string    `json:"sha1"`
	Bucket    string    `json:"bucket"`
	Object    string    `json:"object"`
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	Parts     []ossPart `json:"parts,omitempty"`
	Callback  string    `json:"callback,omitempty"`
	CallbackV string    `json:"callback_var,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
}

type ossPart struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

const p115UploadSessionStateFile = "115_upload_sessions.json"
const p115UploadSessionMaxAge = 24 * time.Hour
const p115UploadSessionMaxEntries = 1024
const p115MultipartPartSize = 16 * 1024 * 1024
const p115MultipartMinSize = p115MultipartPartSize

func init() {
	drive.Register("115", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		return New(Options{
			Cookie:    cookie,
			RootPath:  params["root_path"],
			LimitRate: 2,
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Secret:      true,
			Description: "115 cloud drive authentication cookie (required on first run; later can be loaded from state)",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
	)
}

type Options struct {
	Cookie    string
	RootID    string
	RootPath  string
	LimitRate float64
}

func New(opts Options) *Driver {
	return &Driver{
		rootID:       opts.RootID,
		rootPath:     opts.RootPath,
		cookies:      opts.Cookie,
		limitRate:    opts.LimitRate,
		metrics:      util.NewBuffer(500),
		cookieSource: "config",
	}
}

func (d *Driver) Init(ctx context.Context) error {
	configCookie := d.cookies
	d.loadCookieState()
	if d.cookies == "" {
		return fmt.Errorf("115: Init: missing cookie")
	}
	if d.limitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.limitRate), 1)
	}
	d.cl = driver115.New(
		driver115.UA(fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)),
	)
	d.cl.Client.OnAfterResponse(func(_ *resty.Client, _ *resty.Response) error {
		d.saveCurrentCookiesFromClient()
		return nil
	})
	cred := &driver115.Credential{}
	if err := cred.FromCookie(d.cookies); err != nil {
		d.setLastError(fmt.Sprintf("115: parse cookie: %v", err))
		return fmt.Errorf("115: parse cookie: %w", err)
	}
	d.cl.ImportCredential(cred)
	if err := d.loginCheckWithFallback(ctx, configCookie, func() error {
		return d.loginCheckWithRetry(ctx, d.cl.LoginCheck)
	}); err != nil {
		d.setLastError(fmt.Sprintf("115: login check: %v", err))
		return fmt.Errorf("115: login check: %w", err)
	}
	d.saveCookieState(d.currentCookieHeader(), d.cookieSource)
	d.httpClient = d.cl.Client.GetClient()
	if d.rootID == "" {
		d.rootID = "0"
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, d.rootID, d.rootPath)
		if err != nil {
			return fmt.Errorf("115: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	return nil
}

func (d *Driver) Drop(context.Context) error {
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.bandwidthLimiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.metrics.Events(since), nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	d.debugMu.Lock()
	lastError := d.lastError
	d.debugMu.Unlock()
	extra := map[string]any{
		drive.DebugExtraCredentialSource:   d.cookieSource,
		drive.DebugExtraInstantUploadCount: d.instantUploads.Load(),
	}
	if !d.cookieUpdated.IsZero() {
		extra[drive.DebugExtraCredentialUpdated] = d.cookieUpdated
	}
	health := drive.HealthLevelOK
	if lastError != "" {
		health = drive.HealthLevelDegraded
		extra[drive.DebugExtraLastError] = lastError
	}
	return drive.DebugSnapshot{
		Driver:      "115",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: extra,
	}, nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var info driver115.InfoData
	err := d.recordSDK(ctx, "space", nil, func() error {
		var err error
		info, err = d.cl.GetInfo()
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: space: %v", err))
		return drive.Space{}, err
	}
	return drive.Space{
		Total: info.SpaceInfo.AllTotal.Size,
		Free:  info.SpaceInfo.AllRemain.Size,
	}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, p)
}

func (d *Driver) recordSDK(ctx context.Context, operation string, request map[string]any, fn func() error) error {
	start := time.Now()
	err := fn()
	event := drive.MetricEvent{
		Layer:     "driver.sdk",
		Operation: operation,
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.metrics.Record(ctx, event)
	return err
}

func (d *Driver) loginCheckWithRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = fn()
		if err == nil || !isRetryableLoginCheckError(err) || attempt >= len(loginCheckRetryDelays) {
			return err
		}
		timer := time.NewTimer(loginCheckRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// loginCheckWithFallback verifies the current cookie and, when the persisted
// state cookie fails authentication while a config cookie is available, retries
// with the config cookie alone and replaces the stale 115_cookie.json state.
// A stale state file therefore cannot wedge the mount: the config cookie is
// re-imported and, on success, the working cookie is persisted for later runs.

func (d *Driver) loginCheckWithFallback(ctx context.Context, configCookie string, login func() error) error {
	err := login()
	if err == nil {
		return nil
	}
	if d.cookieSource != "state" || configCookie == "" || configCookie == d.cookies {
		return err
	}
	// The state cookie is stale; fall back to the config cookie.
	d.cookies = configCookie
	d.cookieSource = "config"
	cred := &driver115.Credential{}
	if cerr := cred.FromCookie(d.cookies); cerr != nil {
		return err
	}
	d.cl.ImportCredential(cred)
	if retryErr := login(); retryErr != nil {
		return retryErr
	}
	// The config cookie authenticated; persist it so later restarts do not
	// reuse the stale state cookie again.
	d.saveCookieState(d.currentCookieHeader(), d.cookieSource)
	return nil
}

func (d *Driver) setLastError(value string) {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	d.lastError = value
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	err := d.stateStore.LoadJSON("115_cookie.json", &state)
	if err != nil {
		return
	}
	if state.Cookie != "" {
		d.cookies = mergeCookieHeaders(d.cookies, state.Cookie)
		d.cookieSource = "state"
	}
	d.cookieUpdated = state.UpdatedAt
}

func (d *Driver) saveCurrentCookiesFromClient() {
	cookie := d.currentCookieHeader()
	if cookie == "" {
		return
	}
	d.saveUpdatedCookie(cookie)
}

func (d *Driver) currentCookieHeader() string {
	if d.cl == nil || d.cl.Client == nil {
		return d.cookies
	}
	cookie := d.cookies
	restyCookies := d.cl.Client.Cookies
	if len(restyCookies) > 0 {
		cookie = mergeCookieHeaders(cookie, cookieHeader(restyCookies))
	}
	if hc := d.cl.Client.GetClient(); hc != nil && hc.Jar != nil {
		for _, rawURL := range []string{
			"https://115.com/",
			"https://my.115.com/",
			"https://webapi.115.com/",
			"https://proapi.115.com/",
			"https://passportapi.115.com/",
			"https://uplb.115.com/",
		} {
			u, err := url.Parse(rawURL)
			if err != nil {
				continue
			}
			cookie = mergeCookieHeaders(cookie, cookieHeader(hc.Jar.Cookies(u)))
		}
	}
	return cookie
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	merged := mergeCookieHeaders(d.cookies, cookie)
	if merged == "" || merged == d.cookies {
		return
	}
	d.cookies = merged
	d.cookieSource = "response"
	d.cookieUpdated = time.Now()
	d.saveCookieState(merged, d.cookieSource)
}

func (d *Driver) saveCookieState(cookie, source string) {
	if d.stateStore == nil {
		return
	}
	if cookie == "" {
		return
	}
	if d.cookieUpdated.IsZero() {
		d.cookieUpdated = time.Now()
	}
	d.cookies = cookie
	d.cookieSource = source
	if err := d.stateStore.SaveJSON("115_cookie.json", cookieState{
		Cookie:    cookie,
		UpdatedAt: d.cookieUpdated,
	}); err != nil {
		logging.L.Warnf("[115] save updated cookie state failed: %v", err)
	}
}

func (d *Driver) userAgent() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)

func (d *Driver) waitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func mergeCookieHeaders(base, overlay string) string {
	values := map[string]string{}
	order := []string{}
	for _, cookie := range parseCookieHeader(base) {
		values[cookie.Name] = cookie.Value
		order = append(order, cookie.Name)
	}
	changed := false
	for _, cookie := range parseCookieHeader(overlay) {
		if cookie.Name == "" {
			continue
		}
		if _, ok := values[cookie.Name]; !ok {
			order = append(order, cookie.Name)
		}
		if values[cookie.Name] != cookie.Value {
			values[cookie.Name] = cookie.Value
			changed = true
		}
	}
	if len(order) == 0 {
		return ""
	}
	if !changed {
		return base
	}
	parts := make([]string, 0, len(order))
	seen := map[string]struct{}{}
	for _, name := range order {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}

func parseCookieHeader(cookieHeader string) []*http.Cookie {
	parts := strings.Split(cookieHeader, ";")
	cookies := make([]*http.Cookie, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: key, Value: value})
	}
	return cookies
}

func cookieHeader(cookies []*http.Cookie) string {
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func isRetryableLoginCheckError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
