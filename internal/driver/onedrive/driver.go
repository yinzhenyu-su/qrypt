// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/util"
	"github.com/yinzhenyu/qrypt/internal/httputil"
	"github.com/yinzhenyu/qrypt/internal/retry"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	defaultRegion      = "global"
	defaultRedirectURI = "https://api.oplist.org/onedrive/callback"
	defaultOnlineAPI   = "https://api.oplist.org/onedrive/renewapi"

	oneDriveSmallUploadLimit = 4 * 1024 * 1024
	defaultChunkSize         = 5 * 1024 * 1024
	oneDriveRequestAttempts  = 3
)

type host struct {
	oauth string
	api   string
}

var oneDriveHosts = map[string]host{
	"global": {oauth: "https://login.microsoftonline.com", api: "https://graph.microsoft.com"},
	"cn":     {oauth: "https://login.chinacloudapi.cn", api: "https://microsoftgraph.chinacloudapi.cn"},
	"us":     {oauth: "https://login.microsoftonline.us", api: "https://graph.microsoft.us"},
	"de":     {oauth: "https://login.microsoftonline.de", api: "https://graph.microsoft.de"},
}

type Driver struct {
	drive.UnsupportedOperations

	region       string
	apiBaseURL   string
	oauthBaseURL string
	rootPath     string
	rootID       string
	isSharepoint bool
	siteID       string
	appMode      bool
	tenantID     string
	email        string
	customHost   string

	useOnlineAPI bool
	onlineAPI    string
	clientID     string
	clientSecret string
	redirectURI  string

	mu           sync.RWMutex
	accessToken  string
	refreshToken string

	chunkSize        int64
	disableDiskUsage bool

	client  *http.Client
	limiter *drive.BandwidthLimiter
	metrics *util.Buffer
}

type Options struct {
	Region           string
	APIBaseURL       string
	OAuthBaseURL     string
	RootPath         string
	IsSharepoint     bool
	SiteID           string
	AppMode          bool
	TenantID         string
	Email            string
	CustomHost       string
	UseOnlineAPI     bool
	OnlineAPI        string
	ClientID         string
	ClientSecret     string
	RedirectURI      string
	AccessToken      string
	RefreshToken     string
	ChunkSize        int64
	DisableDiskUsage bool
	HTTPClient       *http.Client
}

func init() {
	drive.Register("onedrive", func(params drive.Params) (drive.Driver, error) {
		refreshToken := params["refresh_token"]
		if refreshToken == "" {
			return nil, fmt.Errorf("onedrive: missing refresh_token")
		}
		chunkSize := int64(0)
		if v := params["chunk_size"]; v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("onedrive: invalid chunk_size: %w", err)
			}
			chunkSize = n * 1024 * 1024
		}
		useOnlineAPI := true
		if v := params["use_online_api"]; v != "" {
			useOnlineAPI = v == "true"
		}
		clientID := params["client_id"]
		if clientID == "" {
			clientID = params["client_key"]
		}
		return New(Options{
			Region:           params["region"],
			APIBaseURL:       params["api_base_url"],
			OAuthBaseURL:     params["oauth_base_url"],
			RootPath:         params["root_path"],
			IsSharepoint:     params["is_sharepoint"] == "true",
			SiteID:           params["site_id"],
			CustomHost:       params["custom_host"],
			UseOnlineAPI:     useOnlineAPI,
			OnlineAPI:        params["online_api"],
			ClientID:         clientID,
			ClientSecret:     params["client_secret"],
			RedirectURI:      params["redirect_uri"],
			AccessToken:      params["access_token"],
			RefreshToken:     refreshToken,
			ChunkSize:        chunkSize,
			DisableDiskUsage: params["disable_disk_usage"] == "true",
		}), nil
	},
		drive.ParamDef{Name: "refresh_token", Type: "string", Required: true, Secret: true, Description: "OneDrive refresh token", Example: "your-refresh-token"},
		drive.ParamDef{Name: "access_token", Type: "string", Secret: true, Description: "Optional initial OneDrive access token; refreshed automatically when needed"},
		drive.ParamDef{Name: "region", Type: "string", Description: "Microsoft cloud region: global, cn, us, or de", Default: "global", Example: "global"},
		drive.ParamDef{Name: "root_path", Type: "string", Description: "OneDrive path used as this mount root", Default: "/", Example: "/qrypt"},
		drive.ParamDef{Name: "use_online_api", Type: "bool", Description: "Use OpenList-compatible online token refresh API", Default: "true"},
		drive.ParamDef{Name: "online_api", Type: "string", Description: "Online token refresh API URL", Example: defaultOnlineAPI},
		drive.ParamDef{Name: "client_id", Type: "string", Secret: true, Description: "OAuth client ID used when use_online_api=false"},
		drive.ParamDef{Name: "client_key", Type: "string", Secret: true, Description: "Alias for client_id"},
		drive.ParamDef{Name: "client_secret", Type: "string", Secret: true, Description: "OAuth client secret used when use_online_api=false"},
		drive.ParamDef{Name: "redirect_uri", Type: "string", Description: "OAuth redirect URI used when your Microsoft app requires it", Example: defaultRedirectURI},
		drive.ParamDef{Name: "api_base_url", Type: "string", Description: "Custom Microsoft Graph API base URL", Example: "https://graph.microsoft.com"},
		drive.ParamDef{Name: "oauth_base_url", Type: "string", Description: "Custom Microsoft OAuth base URL", Example: "https://login.microsoftonline.com"},
		drive.ParamDef{Name: "is_sharepoint", Type: "bool", Description: "Use SharePoint site drive instead of the current user's drive", Default: "false"},
		drive.ParamDef{Name: "site_id", Type: "string", Description: "SharePoint site ID when is_sharepoint=true"},
		drive.ParamDef{Name: "custom_host", Type: "string", Description: "Custom host for download URLs"},
		drive.ParamDef{Name: "chunk_size", Type: "int", Description: "Large upload chunk size in MiB", Default: "5", Example: "10"},
		drive.ParamDef{Name: "disable_disk_usage", Type: "bool", Description: "Disable OneDrive quota query", Default: "false"},
	)
	drive.Register("onedrive_app", func(params drive.Params) (drive.Driver, error) {
		clientID := params["client_id"]
		if clientID == "" {
			clientID = params["client_key"]
		}
		if clientID == "" {
			return nil, fmt.Errorf("onedrive_app: missing client_id")
		}
		if params["client_secret"] == "" {
			return nil, fmt.Errorf("onedrive_app: missing client_secret")
		}
		if params["tenant_id"] == "" {
			return nil, fmt.Errorf("onedrive_app: missing tenant_id")
		}
		if params["email"] == "" {
			return nil, fmt.Errorf("onedrive_app: missing email")
		}
		chunkSize := int64(0)
		if v := params["chunk_size"]; v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("onedrive_app: invalid chunk_size: %w", err)
			}
			chunkSize = n * 1024 * 1024
		}
		return New(Options{
			Region:           params["region"],
			APIBaseURL:       params["api_base_url"],
			OAuthBaseURL:     params["oauth_base_url"],
			RootPath:         params["root_path"],
			AppMode:          true,
			TenantID:         params["tenant_id"],
			Email:            params["email"],
			CustomHost:       params["custom_host"],
			ClientID:         clientID,
			ClientSecret:     params["client_secret"],
			ChunkSize:        chunkSize,
			DisableDiskUsage: params["disable_disk_usage"] == "true",
		}), nil
	},
		drive.ParamDef{Name: "client_id", Type: "string", Required: true, Secret: true, Description: "Microsoft Entra application client ID"},
		drive.ParamDef{Name: "client_key", Type: "string", Secret: true, Description: "Alias for client_id"},
		drive.ParamDef{Name: "client_secret", Type: "string", Required: true, Secret: true, Description: "Microsoft Entra application client secret"},
		drive.ParamDef{Name: "tenant_id", Type: "string", Required: true, Description: "Microsoft Entra tenant ID"},
		drive.ParamDef{Name: "email", Type: "string", Required: true, Description: "User principal name or email whose drive should be mounted", Example: "user@example.com"},
		drive.ParamDef{Name: "region", Type: "string", Description: "Microsoft cloud region: global, cn, us, or de", Default: "global", Example: "global"},
		drive.ParamDef{Name: "root_path", Type: "string", Description: "OneDrive path used as this mount root", Default: "/", Example: "/qrypt"},
		drive.ParamDef{Name: "api_base_url", Type: "string", Description: "Custom Microsoft Graph API base URL", Example: "https://graph.microsoft.com"},
		drive.ParamDef{Name: "oauth_base_url", Type: "string", Description: "Custom Microsoft OAuth base URL", Example: "https://login.microsoftonline.com"},
		drive.ParamDef{Name: "custom_host", Type: "string", Description: "Custom host for download URLs"},
		drive.ParamDef{Name: "chunk_size", Type: "int", Description: "Large upload chunk size in MiB", Default: "5", Example: "10"},
		drive.ParamDef{Name: "disable_disk_usage", Type: "bool", Description: "Disable OneDrive quota query", Default: "false"},
	)
}

func New(opts Options) *Driver {
	region := opts.Region
	if region == "" {
		region = defaultRegion
	}
	h := oneDriveHosts[region]
	if h.api == "" {
		h = oneDriveHosts[defaultRegion]
	}
	apiBaseURL := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBaseURL == "" {
		apiBaseURL = h.api
	}
	oauthBaseURL := strings.TrimRight(opts.OAuthBaseURL, "/")
	if oauthBaseURL == "" {
		oauthBaseURL = h.oauth
	}
	rootPath := cleanOneDrivePath(opts.RootPath)
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	onlineAPI := opts.OnlineAPI
	if onlineAPI == "" {
		onlineAPI = defaultOnlineAPI
	}
	client := opts.HTTPClient
	if client == nil {
		client = httputil.NewClient(0, 60*time.Second)
	}
	return &Driver{
		region:           region,
		apiBaseURL:       apiBaseURL,
		oauthBaseURL:     oauthBaseURL,
		rootPath:         rootPath,
		isSharepoint:     opts.IsSharepoint,
		siteID:           opts.SiteID,
		appMode:          opts.AppMode,
		tenantID:         opts.TenantID,
		email:            opts.Email,
		customHost:       opts.CustomHost,
		useOnlineAPI:     opts.UseOnlineAPI,
		onlineAPI:        onlineAPI,
		clientID:         opts.ClientID,
		clientSecret:     opts.ClientSecret,
		redirectURI:      opts.RedirectURI,
		accessToken:      opts.AccessToken,
		refreshToken:     opts.RefreshToken,
		chunkSize:        chunkSize,
		disableDiskUsage: opts.DisableDiskUsage,
		client:           client,
		metrics:          util.NewBuffer(500),
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.isSharepoint && d.siteID == "" {
		return fmt.Errorf("onedrive: site_id is required when is_sharepoint=true")
	}
	if d.appMode {
		if d.tenantID == "" {
			return fmt.Errorf("onedrive_app: tenant_id is required")
		}
		if d.email == "" {
			return fmt.Errorf("onedrive_app: email is required")
		}
	}
	if d.currentAccessToken() == "" {
		if err := d.refresh(ctx); err != nil {
			return err
		}
	}
	root, err := d.itemByPath(ctx, d.rootPath)
	if err != nil {
		return fmt.Errorf("%s: resolve root_path %q: %w", d.driverName(), d.rootPath, err)
	}
	if root.ID == "" {
		return fmt.Errorf("%s: resolved root_path %q without id", d.driverName(), d.rootPath)
	}
	d.rootID = root.ID
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolveID(parentID)
	var out []drive.Entry
	next := d.apiPath(fmt.Sprintf("/items/%s/children?$top=1000&$select=id,name,size,fileSystemInfo,file,folder,parentReference", url.PathEscape(parentID)))
	for next != "" {
		var resp listResp
		if err := d.requestJSON(ctx, http.MethodGet, next, nil, &resp); err != nil {
			return nil, fmt.Errorf("onedrive: list %q: %w", parentID, err)
		}
		for _, item := range resp.Value {
			out = append(out, item.entry(parentID))
		}
		next = resp.NextLink
	}
	return out, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.IsDir {
		return nil, fmt.Errorf("onedrive: cannot read directory %q", entry.ID)
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("onedrive: invalid range offset=%d size=%d", offset, size)
	}
	if entry.Size > 0 && offset >= entry.Size {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	item, err := d.itemByID(ctx, entry.ID)
	if err != nil {
		return nil, fmt.Errorf("onedrive: get download url %q: %w", entry.ID, err)
	}
	if item.DownloadURL == "" {
		return nil, fmt.Errorf("onedrive: item %q has no download url", entry.ID)
	}
	downloadURL := d.applyCustomHost(item.DownloadURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 || size > 0 {
		end := ""
		if size > 0 {
			endOffset := offset + size - 1
			if entry.Size > 0 && endOffset >= entry.Size {
				endOffset = entry.Size - 1
			}
			end = strconv.FormatInt(endOffset, 10)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%s", offset, end))
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, "download", http.MethodGet, downloadURL, start, respStatus(resp), err)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && entry.Size > 0 && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("onedrive: download status=%d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	rc := resp.Body
	if d.limiter != nil {
		rc = d.limiter.LimitDownload(ctx, rc)
	}
	return rc, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentID = d.resolveID(parentID)
	body := map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "rename",
	}
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodPost, d.apiPath(fmt.Sprintf("/items/%s/children", url.PathEscape(parentID))), body, &item); err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: mkdir %q: %w", name, err)
	}
	return item.entry(parentID), nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := d.requestJSON(ctx, http.MethodDelete, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), nil, nil); err != nil {
		return fmt.Errorf("onedrive: remove %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	body := map[string]any{"name": newName}
	if err := d.requestJSON(ctx, http.MethodPatch, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), body, nil); err != nil {
		return fmt.Errorf("onedrive: rename %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dstParentID = d.resolveID(dstParentID)
	body := map[string]any{
		"parentReference": map[string]any{"id": dstParentID},
		"name":            entry.Name,
	}
	if err := d.requestJSON(ctx, http.MethodPatch, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), body, nil); err != nil {
		return fmt.Errorf("onedrive: move %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID := d.resolveID(req.ParentID)
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: put source open: %w", err)
	}
	defer body.Close()
	if req.Source.Size() <= oneDriveSmallUploadLimit {
		return d.putSmall(ctx, parentID, req.Name, req.Source.Size(), body, req.Progress)
	}
	return d.putLarge(ctx, parentID, req.Name, req.Source.Size(), body, req.Progress)
}

func (d *Driver) putSmall(ctx context.Context, parentID, name string, size int64, body io.Reader, progress drive.UploadProgress) (drive.Entry, error) {
	var uploadBody io.Reader = drive.NewUploadProgressReader(progress, body)
	if d.limiter != nil {
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
	}
	var item itemResp
	path := d.apiPath(fmt.Sprintf("/items/%s:/%s:/content", url.PathEscape(parentID), escapePathSegment(name)))
	if err := d.requestRaw(ctx, http.MethodPut, path, uploadBody, "application/octet-stream", &item); err != nil {
		err = fmt.Errorf("onedrive: put %q: %w", name, err)
		if nonRetryableUploadError(err) {
			err = drive.NonRetryable(err)
		}
		return drive.Entry{}, err
	}
	if item.Size == 0 {
		item.Size = size
	}
	return item.entry(parentID), nil
}

func (d *Driver) putLarge(ctx context.Context, parentID, name string, size int64, body drive.ReadOnlyFile, progress drive.UploadProgress) (drive.Entry, error) {
	var session createUploadSessionResp
	sessionPath := d.apiPath(fmt.Sprintf("/items/%s:/%s:/createUploadSession", url.PathEscape(parentID), escapePathSegment(name)))
	payload := map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"}}
	if err := d.requestJSON(ctx, http.MethodPost, sessionPath, payload, &session); err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q: %w", name, err)
	}
	if session.UploadURL == "" {
		return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q returned empty uploadUrl", name)
	}
	for offset := int64(0); offset < size; offset += d.chunkSize {
		if err := ctx.Err(); err != nil {
			return drive.Entry{}, err
		}
		partSize := d.chunkSize
		if remaining := size - offset; remaining < partSize {
			partSize = remaining
		}
		reader := io.NewSectionReader(body, offset, partSize)
		var uploadBody io.Reader = drive.NewUploadProgressReader(progress, reader)
		if d.limiter != nil {
			uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, uploadBody)
		if err != nil {
			return drive.Entry{}, err
		}
		req.ContentLength = partSize
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+partSize-1, size))
		start := time.Now()
		resp, err := d.client.Do(req)
		d.recordHTTP(ctx, "UploadPart", http.MethodPut, "upload_session", start, respStatus(resp), err)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("onedrive: upload part: %w", err)
		}
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			err := fmt.Errorf("onedrive: upload part status=%d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
				err = drive.NonRetryable(err)
			}
			return drive.Entry{}, err
		}
		resp.Body.Close()
	}
	item, err := d.itemByChildName(ctx, parentID, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: resolve uploaded file %q: %w", name, err)
	}
	return item.entry(parentID), nil
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	p = cleanOneDrivePath(p)
	if p == "/" {
		return d.resolveID(""), nil
	}
	item, err := d.itemByPath(ctx, stdpath.Join(d.rootPath, p))
	if err != nil {
		return "", err
	}
	return item.ID, nil
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	if d.disableDiskUsage {
		return drive.Space{}, drive.ErrSpaceUnsupported
	}
	var resp driveResp
	if err := d.requestJSON(ctx, http.MethodGet, d.driveURL(), nil, &resp); err != nil {
		return drive.Space{}, err
	}
	return drive.Space{Total: resp.Quota.Total, Free: resp.Quota.Remaining}, nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      d.driverName(),
		Health:      drive.HealthLevelOK,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"region":                d.region,
			drive.DebugStatRootPath: d.rootPath,
			drive.DebugStatRootID:   d.rootID,
			"is_sharepoint":         d.isSharepoint,
			"app_mode":              d.appMode,
			"email":                 d.email,
			"chunk_size":            d.chunkSize,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource: credentialSource(d.useOnlineAPI),
		},
	}, nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return drive.NormalizeMetricEvents(d.driverName(), d.metrics.Events(since)), nil
}

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
	}
}

func (d *Driver) refresh(ctx context.Context) error {
	if d.appMode {
		return d.refreshApp(ctx)
	}
	refreshToken := d.currentRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("onedrive: refresh token is required")
	}
	if d.useOnlineAPI {
		err := d.refreshOnline(ctx, refreshToken)
		if err == nil {
			return nil
		}
		if d.clientID == "" || d.clientSecret == "" {
			return err
		}
	}
	return d.refreshOAuth(ctx, refreshToken)
}

func (d *Driver) refreshApp(ctx context.Context) error {
	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("onedrive_app: client_id and client_secret are required")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", d.clientID)
	form.Set("client_secret", d.clientSecret)
	form.Set("resource", d.apiBaseURL+"/")
	form.Set("scope", d.apiBaseURL+"/.default")
	reqBody := strings.NewReader(form.Encode())
	var resp tokenResp
	if err := d.requestNoAuthRaw(ctx, http.MethodPost, d.oauthBaseURL+"/"+url.PathEscape(d.tenantID)+"/oauth2/token", reqBody, "application/x-www-form-urlencoded", &resp); err != nil {
		return fmt.Errorf("onedrive_app: access token: %w", err)
	}
	if resp.AccessToken == "" {
		return fmt.Errorf("onedrive_app: access token returned empty token")
	}
	d.setTokens(resp.AccessToken, "")
	return nil
}

func (d *Driver) refreshOnline(ctx context.Context, refreshToken string) error {
	u, err := url.Parse(d.onlineAPI)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("refresh_ui", refreshToken)
	q.Set("server_use", "true")
	q.Set("driver_txt", "onedrive_pr")
	u.RawQuery = q.Encode()
	var resp onlineTokenResp
	if err := d.requestNoAuthJSON(ctx, http.MethodGet, u.String(), nil, &resp); err != nil {
		return fmt.Errorf("onedrive: refresh token: %w", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		if resp.ErrorMessage != "" {
			return fmt.Errorf("onedrive: refresh token: %s", resp.ErrorMessage)
		}
		return fmt.Errorf("onedrive: refresh token returned empty token")
	}
	d.setTokens(resp.AccessToken, resp.RefreshToken)
	return nil
}

func (d *Driver) refreshOAuth(ctx context.Context, refreshToken string) error {
	if d.clientID == "" || d.clientSecret == "" {
		return fmt.Errorf("onedrive: client_id and client_secret are required when use_online_api=false")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", d.clientID)
	form.Set("client_secret", d.clientSecret)
	form.Set("refresh_token", refreshToken)
	if d.redirectURI != "" {
		form.Set("redirect_uri", d.redirectURI)
	}
	reqBody := strings.NewReader(form.Encode())
	var resp tokenResp
	if err := d.requestNoAuthRaw(ctx, http.MethodPost, d.oauthBaseURL+"/common/oauth2/v2.0/token", reqBody, "application/x-www-form-urlencoded", &resp); err != nil {
		return fmt.Errorf("onedrive: refresh token: %w", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		return fmt.Errorf("onedrive: refresh token returned empty token")
	}
	d.setTokens(resp.AccessToken, resp.RefreshToken)
	return nil
}

func (d *Driver) itemByPath(ctx context.Context, p string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.metaURL(p), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) itemByID(ctx context.Context, id string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.apiPath(fmt.Sprintf("/items/%s?$select=id,name,size,fileSystemInfo,file,folder,parentReference,@microsoft.graph.downloadUrl", url.PathEscape(id))), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) itemByChildName(ctx context.Context, parentID, name string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.apiPath(fmt.Sprintf("/items/%s:/%s:?$select=id,name,size,fileSystemInfo,file,folder,parentReference", url.PathEscape(parentID), escapePathSegment(name))), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) requestJSON(ctx context.Context, method, rawURL string, body, result any) error {
	err := d.requestJSONNoRefresh(ctx, method, rawURL, body, result)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Code == "InvalidAuthenticationToken" {
		if refreshErr := d.refresh(ctx); refreshErr != nil {
			return refreshErr
		}
		return d.requestJSONNoRefresh(ctx, method, rawURL, body, result)
	}
	return err
}

func (d *Driver) requestJSONNoRefresh(ctx context.Context, method, rawURL string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	return d.requestRaw(ctx, method, rawURL, reader, "application/json", result)
}

func (d *Driver) requestRaw(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any) error {
	return d.requestRawWithAuth(ctx, method, rawURL, body, contentType, result, d.currentAccessToken())
}

func (d *Driver) requestNoAuthJSON(ctx context.Context, method, rawURL string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	return d.requestNoAuthRaw(ctx, method, rawURL, reader, "application/json", result)
}

func (d *Driver) requestNoAuthRaw(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any) error {
	return d.requestRawWithAuth(ctx, method, rawURL, body, contentType, result, "")
}

func (d *Driver) requestRawWithAuth(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any, accessToken string) error {
	var lastErr error
	for attempt := 0; attempt < oneDriveRequestAttempts; attempt++ {
		err := d.requestRawOnce(ctx, method, rawURL, body, contentType, result, accessToken)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableOneDriveError(ctx, err) || attempt == oneDriveRequestAttempts-1 {
			return err
		}
		if waitErr := retry.WaitExponential(ctx, attempt); waitErr != nil {
			return waitErr
		}
	}
	return lastErr
}

func (d *Driver) requestRawOnce(ctx context.Context, method, rawURL string, body io.Reader, contentType string, result any, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	if contentType != "" && body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, method, method, rawURL, start, respStatus(resp), err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var graphErr graphErrorResp
		_ = json.Unmarshal(data, &graphErr)
		if graphErr.Error.Code != "" || graphErr.Error.Message != "" {
			return &apiError{Status: resp.StatusCode, Code: graphErr.Error.Code, Message: graphErr.Error.Message}
		}
		return &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	if result != nil && len(data) > 0 {
		if err := json.Unmarshal(data, result); err != nil {
			return err
		}
	}
	return nil
}

func retryableOneDriveError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func nonRetryableUploadError(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusRequestTimeout && apiErr.Status != http.StatusTooManyRequests
	}
	return false
}

func (d *Driver) metaURL(p string) string {
	p = cleanOneDrivePath(p)
	if p == "/" {
		return d.apiPath("/root")
	}
	return d.apiPath("/root:" + escapeDrivePath(p) + ":")
}

func (d *Driver) apiPath(suffix string) string {
	if d.appMode {
		return d.apiBaseURL + "/v1.0/users/" + url.PathEscape(d.email) + "/drive" + suffix
	}
	if d.isSharepoint {
		return d.apiBaseURL + "/v1.0/sites/" + url.PathEscape(d.siteID) + "/drive" + suffix
	}
	return d.apiBaseURL + "/v1.0/me/drive" + suffix
}

func (d *Driver) driveURL() string {
	if d.appMode {
		return d.apiBaseURL + "/v1.0/users/" + url.PathEscape(d.email) + "/drive"
	}
	if d.isSharepoint {
		return d.apiBaseURL + "/v1.0/sites/" + url.PathEscape(d.siteID) + "/drive"
	}
	return d.apiBaseURL + "/v1.0/me/drive"
}

func (d *Driver) resolveID(id string) string {
	if id == "" || id == "0" || id == "/" || id == "root" {
		if d.rootID != "" {
			return d.rootID
		}
		return "root"
	}
	return id
}

func (d *Driver) applyCustomHost(rawURL string) string {
	if d.customHost == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Host = d.customHost
	return u.String()
}

func (d *Driver) currentAccessToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.accessToken
}

func (d *Driver) currentRefreshToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.refreshToken
}

func (d *Driver) setTokens(accessToken, refreshToken string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.accessToken = accessToken
	d.refreshToken = refreshToken
}

func (d *Driver) recordHTTP(ctx context.Context, operation, method, rawURL string, start time.Time, status int, err error) {
	event := drive.MetricEvent{
		Layer:     "driver.http",
		Operation: operation,
		Method:    method,
		Status:    status,
		Duration:  time.Since(start).String(),
	}
	if rawURL != "" && !strings.Contains(rawURL, "uploadUrl=") {
		event.URL = rawURL
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.metrics.Record(ctx, event)
}

func respStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func cleanOneDrivePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return "/"
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/." {
		return "/"
	}
	return stdpath.Clean(p)
}

func escapeDrivePath(p string) string {
	p = cleanOneDrivePath(p)
	if p == "/" {
		return "/"
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i := range parts {
		parts[i] = escapePathSegment(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func escapePathSegment(segment string) string {
	return strings.ReplaceAll(url.PathEscape(segment), "+", "%20")
}

func credentialSource(useOnlineAPI bool) string {
	if useOnlineAPI {
		return "online_api"
	}
	return "oauth"
}

func (d *Driver) driverName() string {
	if d.appMode {
		return "onedrive_app"
	}
	return "onedrive"
}

var (
	_ drive.Driver = (*Driver)(nil)
)
