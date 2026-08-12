package quark

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func init() {
	drive.Register("quark", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("quark: missing cookie")
		}
		return New(cookie, Options{
			RootPath: params["root_path"],
			BaseURL:  params["base_url"],
			V2URL:    params["v2_url"],
		}), nil
	},
		drive.ParamDef{
			Name:        "cookie",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Quark cloud drive authentication cookie",
			Example:     "k1=v1; k2=v2",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path on the drive",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "base_url",
			Type:        "string",
			Description: "Custom API base URL",
			Example:     "https://drive.quark.cn",
		},
		drive.ParamDef{
			Name:        "v2_url",
			Type:        "string",
			Description: "Custom API v2 URL",
			Example:     "https://drive-m.quark.cn",
		},
	)
}

func New(cookie string, opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = "0"
	}
	d := &Driver{
		cl:           newClient(cookie, clientOptions{BaseURL: opts.BaseURL, V2URL: opts.V2URL}),
		cookie:       cookie,
		rootPath:     opts.RootPath,
		rootID:       rootID,
		cookieSource: "config",
		debugUploads: map[string]quarkUploadDebug{},
	}
	d.cl.onCookieUpdate = d.saveUpdatedCookie
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	configCookie := d.cookie
	d.loadCookieState()
	if d.cookie == "" {
		return fmt.Errorf("quark: cookie is required")
	}
	if err := d.validateWithFallback(ctx, configCookie, func() error {
		var resp sortResp
		if err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
			"pdir_fid": d.rootID,
			"_size":    "1",
		}, nil, &resp); err != nil {
			return err
		}
		return apiError(resp.respEnvelope)
	}); err != nil {
		return fmt.Errorf("quark: validate cookie: %w", err)
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.resolvePathFrom(ctx, "0", d.rootPath)
		if err != nil {
			return fmt.Errorf("quark: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error {
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.pruneStoredUploadSessions()
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

type Driver struct {
	drive.UnsupportedOperations
	cl                 *client
	urlCache           sync.Map
	cookie             string
	rootPath           string
	rootID             string
	limiter            *drive.BandwidthLimiter
	stateStore         drive.StateStore
	cookieSource       string
	cookieUpdated      time.Time
	debugMu            sync.Mutex
	debugUploads       map[string]quarkUploadDebug
	lastError          string
	instantUploadCount int64
}

type Options struct {
	RootPath string
	RootID   string
	BaseURL  string
	V2URL    string
}

type cookieState struct {
	Cookie    string    `json:"cookie,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

const quarkUploadSessionStateFile = "quark_upload_sessions.json"

const quarkUploadSessionMaxAge = 24 * time.Hour

const quarkUploadSessionMaxEntries = 1024

const quarkDownloadMaxRetries = 3

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashMD5, drive.HashSHA1}
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, path)
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	activeUploads := d.activeUploadDebug()
	urlCacheCount := 0
	d.urlCache.Range(func(_, _ any) bool {
		urlCacheCount++
		return true
	})
	health := "ok"
	if d.getLastError() != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "quark",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"active_uploads":        len(activeUploads),
			"url_cache_count":       urlCacheCount,
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.cookieSource,
			drive.DebugExtraCredentialUpdated:  d.cookieUpdated,
			drive.DebugExtraLastError:          d.getLastError(),
			drive.DebugExtraInstantUploadCount: d.instantUploadCount,
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) getLastError() string {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	return d.lastError
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp struct {
		respEnvelope
		Data struct {
			Total int64 `json:"total_capacity"`
			Used  int64 `json:"use_capacity"`
		} `json:"data"`
	}
	err := d.cl.request(ctx, http.MethodGet, "/member", map[string]string{
		"uc_param_str":    "",
		"fetch_subscribe": "true",
		"_ch":             "home",
		"fetch_identity":  "true",
	}, nil, &resp)
	if err != nil {
		return drive.Space{}, fmt.Errorf("quark: space: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return drive.Space{}, fmt.Errorf("quark: space: %w", err)
	}
	return drive.Space{
		Total: resp.Data.Total,
		Free:  resp.Data.Total - resp.Data.Used,
	}, nil
}

func (d *Driver) loadCookieState() {
	if d.stateStore == nil {
		return
	}
	var state cookieState
	err := d.stateStore.LoadJSON("quark_cookie.json", &state)
	if err != nil {
		return
	}
	if state.Cookie != "" {
		d.cookie = state.Cookie
		d.cl.setCookie(state.Cookie)
		d.cookieSource = "state"
	}
	d.cookieUpdated = state.UpdatedAt
}

// validateWithFallback runs validate with the current cookie and, when the
// persisted state cookie fails while a config cookie is available, retries
// with the config cookie alone and replaces the stale quark_cookie.json.
// A stale state file therefore cannot wedge the mount: only when both the
// state and the config cookie fail is the error returned.

func (d *Driver) validateWithFallback(ctx context.Context, configCookie string, validate func() error) error {
	err := validate()
	if err == nil {
		return nil
	}
	if d.cookieSource != "state" || configCookie == "" || configCookie == d.cookie {
		return err
	}
	// The state cookie is stale; fall back to the config cookie.
	d.cookie = configCookie
	d.cl.setCookie(configCookie)
	d.cookieSource = "config"
	if retryErr := validate(); retryErr != nil {
		return retryErr
	}
	// The config cookie authenticated; persist it so later restarts do not
	// reuse the stale state cookie again.
	d.saveUpdatedCookie(d.cookie)
	return nil
}

func (d *Driver) saveUpdatedCookie(cookie string) {
	if cookie == "" {
		return
	}
	d.cookie = cookie
	d.cookieSource = "response"
	d.cookieUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON("quark_cookie.json", cookieState{
		Cookie:    cookie,
		UpdatedAt: d.cookieUpdated,
	}); err != nil {
		logging.L.Warnf("[QUARK] save updated cookie state failed: %v", err)
	}
}

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func quarkDurationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	ms := d.Milliseconds()
	if ms == 0 {
		return 1
	}
	return ms
}

var _ drive.BandwidthLimitInstaller = (*Driver)(nil)

var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
