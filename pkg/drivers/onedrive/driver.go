// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	stdpath "path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httputil"
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
	metrics *driverutil.Buffer
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
		metrics:          driverutil.NewBuffer(500),
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
		drive.CapabilityServerSideCopy,
		drive.CapabilitySourceUploader,
		drive.CapabilitySpace,
		drive.CapabilityWriter,
	}
}

// RemoteHash reports the content SHA1 from the Graph API file hashes.

func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	return drive.RemoteHashFromExtra(entry, "sha1", drive.HashSHA1)
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

func (d *Driver) driverName() string {
	if d.appMode {
		return "onedrive_app"
	}
	return "onedrive"
}

var (
	_ drive.Driver = (*Driver)(nil)
)

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
		return httputil.IsNonRetryableClientStatus(apiErr.Status)
	}
	return false
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
