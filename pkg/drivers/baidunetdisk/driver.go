package baidunetdisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/util"
	"github.com/yinzhenyu/qrypt/internal/util/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	defaultAPIBaseURL    = "https://pan.baidu.com/rest/2.0"
	defaultOAuthURL      = "https://openapi.baidu.com/oauth/2.0/token"
	defaultOnlineAPI     = "https://api.oplist.org/baiduyun/renewapi"
	defaultUploadAPI     = "https://d.pcs.baidu.com"
	defaultRootPath      = "/"
	defaultDownloadUA    = "pan.baidu.com"
	defaultDownloadTTL   = time.Hour
	defaultTokenSkew     = 5 * time.Minute
	defaultListPageLimit = 1000
	defaultUploadPart    = 4 << 20
	maxUploadParts       = 2048
	firstSliceMD5Size    = 256 << 10
)

type Driver struct {
	drive.UnsupportedOperations
	httpClient         *http.Client
	refreshToken       string
	accessToken        string
	configRefreshToken string
	clientID           string
	clientSecret       string
	rootPath           string
	orderBy            string
	orderDesc          bool
	apiBaseURL         string
	oauthURL           string
	onlineAPI          string
	uploadAPI          string
	useOnlineAPI       bool
	downloadUA         string
	limiter            *drive.BandwidthLimiter
	stateStore         drive.StateStore
	tokenSource        string
	tokenUpdated       time.Time
	tokenMu            sync.Mutex
	tokenExpires       time.Time
	downloadCache      sync.Map
	lastErrorMu        sync.Mutex
	lastError          string
	instantUploadCount int64
	metrics            *util.Buffer
}

var errBaiduUploadIDExpired = errors.New("baidu_netdisk: uploadid expired")

type Options struct {
	RefreshToken string
	AccessToken  string
	ClientID     string
	ClientSecret string
	RootPath     string
	OrderBy      string
	OrderDesc    bool
	APIBaseURL   string
	OAuthURL     string
	OnlineAPI    string
	UploadAPI    string
	UseOnlineAPI bool
	DownloadUA   string
}

type cachedDownloadURL struct {
	URL       string
	ExpiresAt time.Time
}

type tokenState struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	// ConfigRefreshToken records the config refresh token that produced this
	// state, so a rotated pair (state newer than config) can be distinguished
	// from a user-swapped config token (config newer than state).
	ConfigRefreshToken string    `json:"config_refresh_token,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type baiduUploadSessionState struct {
	Version  int                           `json:"version"`
	Sessions map[string]baiduUploadSession `json:"sessions,omitempty"`
}

type baiduUploadSession struct {
	Key            string       `json:"key"`
	ParentPath     string       `json:"parent_path"`
	Name           string       `json:"name"`
	RemotePath     string       `json:"remote_path"`
	Size           int64        `json:"size"`
	ContentMD5     string       `json:"content_md5"`
	SliceMD5       string       `json:"slice_md5"`
	UploadID       string       `json:"upload_id"`
	PartSize       int64        `json:"part_size"`
	BlockList      []int        `json:"block_list,omitempty"`
	CompletedParts map[int]bool `json:"completed_parts,omitempty"`
	SavedAt        time.Time    `json:"saved_at"`
}

const baiduUploadSessionStateFile = "baidu_netdisk_upload_sessions.json"
const baiduUploadSessionMaxAge = 24 * time.Hour
const baiduUploadSessionMaxEntries = 1024

func init() {
	drive.Register("baidu_netdisk", func(params drive.Params) (drive.Driver, error) {
		refreshToken := params["refresh_token"]
		if refreshToken == "" {
			return nil, fmt.Errorf("baidu_netdisk: missing refresh_token")
		}
		useOnlineAPI := true
		if raw := params["use_online_api"]; raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("baidu_netdisk: invalid use_online_api: %w", err)
			}
			useOnlineAPI = parsed
		}
		orderDesc := false
		switch strings.ToLower(params["order_direction"]) {
		case "", "asc":
		case "desc":
			orderDesc = true
		default:
			return nil, fmt.Errorf("baidu_netdisk: order_direction must be asc or desc")
		}
		return New(Options{
			RefreshToken: refreshToken,
			AccessToken:  params["access_token"],
			ClientID:     params["client_id"],
			ClientSecret: params["client_secret"],
			RootPath:     params["root_path"],
			OrderBy:      params["order_by"],
			OrderDesc:    orderDesc,
			APIBaseURL:   params["api_base_url"],
			OAuthURL:     params["oauth_url"],
			OnlineAPI:    params["online_api"],
			UploadAPI:    params["upload_api"],
			UseOnlineAPI: useOnlineAPI,
			DownloadUA:   params["download_user_agent"],
		}), nil
	},
		drive.ParamDef{Name: "refresh_token", Type: "string", Required: true, Secret: true, Description: "Baidu Netdisk refresh token", Example: "your-refresh-token"},
		drive.ParamDef{Name: "access_token", Type: "string", Secret: true, Description: "Optional initial access token; refreshed automatically when needed"},
		drive.ParamDef{Name: "root_path", Type: "string", Description: "Baidu Netdisk path used as this mount root", Default: "/", Example: "/qrypt"},
		drive.ParamDef{Name: "order_by", Type: "string", Description: "List ordering field: name, time, or size", Default: "name"},
		drive.ParamDef{Name: "order_direction", Type: "string", Description: "List ordering direction: asc or desc", Default: "asc"},
		drive.ParamDef{Name: "use_online_api", Type: "bool", Description: "Use OpenList-compatible online token refresh API", Default: "true"},
		drive.ParamDef{Name: "online_api", Type: "string", Description: "Online token refresh API URL", Default: defaultOnlineAPI},
		drive.ParamDef{Name: "upload_api", Type: "string", Description: "Baidu PCS upload API base URL", Default: defaultUploadAPI},
		drive.ParamDef{Name: "client_id", Type: "string", Secret: true, Description: "Baidu app API Key used as OAuth client_id when use_online_api=false"},
		drive.ParamDef{Name: "client_secret", Type: "string", Secret: true, Description: "Baidu app Secret Key used as OAuth client_secret when use_online_api=false"},
		drive.ParamDef{Name: "api_base_url", Type: "string", Description: "Custom Baidu REST API base URL", Default: defaultAPIBaseURL},
		drive.ParamDef{Name: "oauth_url", Type: "string", Description: "Custom Baidu OAuth token URL", Default: defaultOAuthURL},
		drive.ParamDef{Name: "download_user_agent", Type: "string", Description: "User-Agent used for Baidu download requests", Default: defaultDownloadUA},
	)
}

func New(opts Options) *Driver {
	rootPath := normalizeDir(opts.RootPath)
	if rootPath == "" {
		rootPath = defaultRootPath
	}
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	oauthURL := opts.OAuthURL
	if oauthURL == "" {
		oauthURL = defaultOAuthURL
	}
	onlineAPI := opts.OnlineAPI
	if onlineAPI == "" {
		onlineAPI = defaultOnlineAPI
	}
	uploadAPI := strings.TrimRight(opts.UploadAPI, "/")
	if uploadAPI == "" {
		uploadAPI = defaultUploadAPI
	}
	downloadUA := opts.DownloadUA
	if downloadUA == "" {
		downloadUA = defaultDownloadUA
	}
	return &Driver{
		httpClient:         &http.Client{Timeout: 60 * time.Second},
		refreshToken:       opts.RefreshToken,
		accessToken:        opts.AccessToken,
		configRefreshToken: opts.RefreshToken,
		clientID:           opts.ClientID,
		clientSecret:       opts.ClientSecret,
		rootPath:           rootPath,
		orderBy:            opts.OrderBy,
		orderDesc:          opts.OrderDesc,
		apiBaseURL:         apiBaseURL,
		oauthURL:           oauthURL,
		onlineAPI:          onlineAPI,
		uploadAPI:          uploadAPI,
		useOnlineAPI:       opts.UseOnlineAPI,
		downloadUA:         downloadUA,
		tokenSource:        "config",
		metrics:            util.NewBuffer(500),
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.refreshToken == "" {
		return fmt.Errorf("baidu_netdisk: refresh_token is required")
	}
	d.loadTokenState()
	if !d.useOnlineAPI && (d.clientID == "" || d.clientSecret == "") {
		return fmt.Errorf("baidu_netdisk: client_id and client_secret are required when use_online_api=false")
	}
	if d.accessToken == "" || d.tokenExpires.IsZero() || time.Now().After(d.tokenExpires.Add(-defaultTokenSkew)) {
		if err := d.refresh(ctx); err != nil {
			d.setLastError(err)
			return err
		}
	}
	if d.rootPath != "/" {
		if _, err := d.statRoot(ctx); err != nil {
			d.setLastError(err)
			return fmt.Errorf("baidu_netdisk: validate root_path %q: %w", d.rootPath, err)
		}
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.metrics.Events(since), nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	d.lastErrorMu.Lock()
	lastError := d.lastError
	instantUploadCount := d.instantUploadCount
	d.lastErrorMu.Unlock()
	health := "ok"
	if lastError != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "baidu_netdisk",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootPath: d.rootPath,
			"order_by":              d.orderBy,
			"order_desc":            d.orderDesc,
			"use_online_api":        d.useOnlineAPI,
			"upload_api":            d.uploadAPI,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.tokenSource,
			drive.DebugExtraCredentialUpdated:  d.tokenUpdated,
			drive.DebugExtraLastError:          lastError,
			drive.DebugExtraInstantUploadCount: instantUploadCount,
		},
	}, nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp quotaResp
	if err := d.request(ctx, http.MethodGet, "https://pan.baidu.com/api/quota", nil, nil, &resp); err != nil {
		err = fmt.Errorf("baidu_netdisk: space: %w", err)
		d.setLastError(err)
		return drive.Space{}, err
	}
	return drive.Space{Total: resp.Total, Free: resp.Total - resp.Used}, nil
}

// RemoteHash reports the content MD5 the API returned when the entry was
// listed (baidu computes md5 of the stored bytes; large files may lack it).

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	if p == "" || p == "/" {
		return d.rootPath, nil
	}
	return normalizeDir(path.Join(d.rootPath, strings.Trim(p, "/"))), nil
}

func (d *Driver) setLastError(err error) {
	d.lastErrorMu.Lock()
	defer d.lastErrorMu.Unlock()
	if err == nil {
		d.lastError = ""
		return
	}
	d.lastError = err.Error()
}

type apiError struct {
	errno   int
	message string
}

func (d *Driver) recordHTTP(ctx context.Context, operation string, req *http.Request, resp *http.Response, start time.Time, request map[string]any, err error) {
	event := drive.MetricEvent{
		Operation: operation,
		Method:    req.Method,
		URL:       util.URL(req.URL),
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if resp != nil {
		event.Status = resp.StatusCode
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.metrics.Record(ctx, event)
}

func (d *Driver) ensureToken(ctx context.Context) error {
	if d.accessToken == "" || (!d.tokenExpires.IsZero() && time.Now().After(d.tokenExpires.Add(-defaultTokenSkew))) {
		return d.refresh(ctx)
	}
	return nil
}

func (d *Driver) refresh(ctx context.Context) error {
	d.tokenMu.Lock()
	defer d.tokenMu.Unlock()
	if d.refreshToken == "" {
		return fmt.Errorf("baidu_netdisk: refresh_token is required")
	}
	var resp tokenResp
	if d.useOnlineAPI {
		u, err := url.Parse(d.onlineAPI)
		if err != nil {
			return err
		}
		query := u.Query()
		query.Set("refresh_ui", d.refreshToken)
		query.Set("server_use", "true")
		query.Set("driver_txt", "baiduyun_go")
		u.RawQuery = query.Encode()
		if err := d.requestToken(ctx, http.MethodGet, u.String(), nil, &resp); err != nil {
			return fmt.Errorf("baidu_netdisk: refresh token via online_api: %w; if this is a normal Baidu OAuth refresh token, set use_online_api=false and configure client_id/client_secret", err)
		}
	} else {
		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", d.refreshToken)
		form.Set("client_id", d.clientID)
		form.Set("client_secret", d.clientSecret)
		if err := d.requestToken(ctx, http.MethodGet, d.oauthURL+"?"+form.Encode(), nil, &resp); err != nil {
			return fmt.Errorf("baidu_netdisk: refresh token: %w", err)
		}
	}
	if resp.Error != "" {
		if resp.Error == "invalid_client" {
			return fmt.Errorf("baidu_netdisk: refresh token: %s: %s; client_id must be the Baidu app API Key and client_secret must be the app Secret Key", resp.Error, resp.ErrorDesc)
		}
		return fmt.Errorf("baidu_netdisk: refresh token: %s: %s", resp.Error, resp.ErrorDesc)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		if resp.ErrorMessage != "" {
			if d.useOnlineAPI {
				return fmt.Errorf("baidu_netdisk: refresh token via online_api: %s; if this is a normal Baidu OAuth refresh token, set use_online_api=false and configure client_id/client_secret", resp.ErrorMessage)
			}
			return fmt.Errorf("baidu_netdisk: refresh token: %s", resp.ErrorMessage)
		}
		return fmt.Errorf("baidu_netdisk: refresh token returned empty token")
	}
	d.accessToken = resp.AccessToken
	d.refreshToken = resp.RefreshToken
	if resp.ExpiresIn > 0 {
		d.tokenExpires = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	} else {
		d.tokenExpires = time.Now().Add(time.Hour)
	}
	d.tokenSource = "refresh"
	d.tokenUpdated = time.Now()
	if err := d.saveTokenState(); err != nil {
		return fmt.Errorf("baidu_netdisk: save token state: %w", err)
	}
	return nil
}

func (d *Driver) loadTokenState() {
	if d.stateStore == nil {
		return
	}
	var state tokenState
	err := d.stateStore.LoadJSON("baidu_netdisk_token.json", &state)
	if err != nil {
		if !drive.IsStateNotExist(err) {
			d.setLastError(fmt.Errorf("baidu_netdisk: load token state: %w", err))
		}
		return
	}
	// The state wins when it was derived from the current config token (token
	// rotation) or when it predates the source marker. A config token that
	// differs from the token the state was derived from is an account switch.
	stateDerived := state.ConfigRefreshToken == "" || state.ConfigRefreshToken == d.configRefreshToken
	if state.RefreshToken != "" && stateDerived {
		d.refreshToken = state.RefreshToken
		d.tokenSource = "state"
	}
	if state.AccessToken != "" && stateDerived {
		d.accessToken = state.AccessToken
	}
	if !state.ExpiresAt.IsZero() && stateDerived {
		d.tokenExpires = state.ExpiresAt
	}
	d.tokenUpdated = state.UpdatedAt
}

func (d *Driver) saveTokenState() error {
	if d.stateStore == nil {
		return nil
	}
	return d.stateStore.SaveJSON("baidu_netdisk_token.json", tokenState{
		AccessToken:        d.accessToken,
		RefreshToken:       d.refreshToken,
		ExpiresAt:          d.tokenExpires,
		ConfigRefreshToken: d.configRefreshToken,
		UpdatedAt:          d.tokenUpdated,
	})
}

func (d *Driver) requestToken(ctx context.Context, method, rawURL string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	start := time.Now()
	resp, err := d.httpClient.Do(req)
	d.recordHTTP(ctx, "token", req, resp, start, nil, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return util.HTTPError("baidu_netdisk: token", req, resp, data)
	}
	return json.Unmarshal(data, out)
}

func (d *Driver) manage(ctx context.Context, op string, filelist any) error {
	data, err := json.Marshal(filelist)
	if err != nil {
		return err
	}
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "filemanager", "opera": op}, map[string]string{
		"async":    "0",
		"filelist": string(data),
		"ondup":    "fail",
	}, nil)
}

func (d *Driver) statRoot(ctx context.Context) (drive.Entry, error) {
	parent := path.Dir(d.rootPath)
	name := path.Base(d.rootPath)
	entries, err := d.listDir(ctx, parent)
	if err != nil {
		return drive.Entry{}, err
	}
	for _, entry := range entries {
		if entry.Name == name && entry.IsDir {
			return entry, nil
		}
	}
	return drive.Entry{}, fmt.Errorf("%w: path not found", drive.ErrNotFound)
}

func normalizeDir(p string) string {
	if p == "" {
		return ""
	}
	cleaned := path.Clean("/" + strings.TrimSpace(p))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func baseName(p string) string {
	if p == "" || p == "/" {
		return ""
	}
	return path.Base(p)
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)

func (e apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("baidu api errno %d", e.errno)
	}
	return fmt.Sprintf("baidu api errno %d: %s", e.errno, e.message)
}

func tokenExpired(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.errno == 111 || apiErr.errno == -6
}

func responseErrno(data []byte) (int, string) {
	var resp struct {
		Errno  *int   `json:"errno"`
		Errmsg string `json:"errmsg"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Errno == nil {
		return 0, ""
	}
	return *resp.Errno, resp.Errmsg
}

func (d *Driver) loadUploadSession(key string) (baiduUploadSession, bool) {
	session, ok := d.uploadSessionStore().Load(key)
	if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	return session, ok
}

func (d *Driver) saveUploadSession(session baiduUploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) uploadSessionStore() *uploadsession.UploadSessionStore[baiduUploadSession] {
	return uploadsession.NewUploadSessionStore(uploadsession.UploadSessionStoreOptions[baiduUploadSession]{
		Store:      d.stateStore,
		File:       baiduUploadSessionStateFile,
		MaxAge:     baiduUploadSessionMaxAge,
		MaxEntries: baiduUploadSessionMaxEntries,
		Key: func(session baiduUploadSession) string {
			return session.Key
		},
		Valid: func(key string, session baiduUploadSession) bool {
			return session.Key != "" && session.UploadID != "" && len(session.BlockList) > 0 && len(session.CompletedParts) > 0
		},
		UpdatedAt: func(session baiduUploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *baiduUploadSession, now time.Time) {
			session.SavedAt = now
		},
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("baidu_netdisk: upload session state: %w", err))
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (errors.Is(err, errBaiduUploadIDExpired) || drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("baidu_netdisk: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
}
