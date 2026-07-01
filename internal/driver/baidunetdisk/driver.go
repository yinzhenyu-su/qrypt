package baidunetdisk

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

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
	httpClient    *http.Client
	refreshToken  string
	accessToken   string
	clientID      string
	clientSecret  string
	rootPath      string
	orderBy       string
	orderDesc     bool
	apiBaseURL    string
	oauthURL      string
	onlineAPI     string
	uploadAPI     string
	useOnlineAPI  bool
	downloadUA    string
	limiter       *drive.BandwidthLimiter
	stateStore    drive.StateStore
	tokenSource   string
	tokenUpdated  time.Time
	tokenMu       sync.Mutex
	tokenExpires  time.Time
	downloadCache sync.Map
	lastErrorMu   sync.Mutex
	lastError     string
}

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
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

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
		httpClient:   &http.Client{Timeout: 60 * time.Second},
		refreshToken: opts.RefreshToken,
		accessToken:  opts.AccessToken,
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		rootPath:     rootPath,
		orderBy:      opts.OrderBy,
		orderDesc:    opts.OrderDesc,
		apiBaseURL:   apiBaseURL,
		oauthURL:     oauthURL,
		onlineAPI:    onlineAPI,
		uploadAPI:    uploadAPI,
		useOnlineAPI: opts.UseOnlineAPI,
		downloadUA:   downloadUA,
		tokenSource:  "config",
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

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	dir := d.resolvePath(parentID)
	return d.listDir(ctx, dir)
}

func (d *Driver) listDir(ctx context.Context, dir string) ([]drive.Entry, error) {
	start := 0
	entries := make([]drive.Entry, 0)
	for {
		query := map[string]string{
			"method": "list",
			"dir":    dir,
			"web":    "web",
			"start":  strconv.Itoa(start),
			"limit":  strconv.Itoa(defaultListPageLimit),
		}
		if d.orderBy != "" {
			query["order"] = d.orderBy
			if d.orderDesc {
				query["desc"] = "1"
			}
		}
		var resp listResp
		if err := d.get(ctx, "/xpan/file", query, &resp); err != nil {
			err = fmt.Errorf("baidu_netdisk: list %q: %w", dir, err)
			d.setLastError(err)
			return nil, err
		}
		for _, item := range resp.List {
			entries = append(entries, item.entry(dir))
		}
		if len(resp.List) < defaultListPageLimit {
			break
		}
		start += defaultListPageLimit
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("baidu_netdisk: read: negative offset or size")
	}
	if entry.Size > 0 && offset >= entry.Size {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	u, err := d.downloadURL(ctx, entry)
	if err != nil {
		d.setLastError(err)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", d.downloadUA)
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
	resp, err := d.httpClient.Do(req)
	if err != nil {
		d.downloadCache.Delete(entry.ID)
		err = fmt.Errorf("baidu_netdisk: read: %w", err)
		d.setLastError(err)
		return nil, err
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && entry.Size > 0 && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		d.downloadCache.Delete(entry.ID)
		err := fmt.Errorf("baidu_netdisk: read status %d", resp.StatusCode)
		d.setLastError(err)
		return nil, err
	}
	return d.limiter.LimitDownload(ctx, resp.Body), nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentPath := d.resolvePath(parentID)
	newPath := path.Join(parentPath, name)
	var resp createResp
	if err := d.create(ctx, newPath, 0, 1, &resp); err != nil {
		err = fmt.Errorf("baidu_netdisk: mkdir %q: %w", newPath, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	entry := drive.Entry{ID: newPath, ParentID: parentPath, Name: name, IsDir: true}
	if resp.File.Path != "" {
		entry = resp.File.entry(parentPath)
	} else if resp.Path != "" {
		entry.ID = resp.Path
	}
	if resp.FsID > 0 {
		entry.Extra = map[string]any{"fs_id": strconv.FormatInt(resp.FsID, 10)}
	}
	return entry, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dst := d.resolvePath(dstParentID)
	err := d.manage(ctx, "move", []map[string]string{{"path": entry.ID, "dest": dst, "newname": entry.Name}})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: move %q to %q: %w", entry.ID, dst, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	err := d.manage(ctx, "rename", []map[string]string{{"path": entry.ID, "newname": newName}})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: rename %q: %w", entry.ID, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	err := d.manage(ctx, "delete", []string{entry.ID})
	if err != nil {
		err = fmt.Errorf("baidu_netdisk: remove %q: %w", entry.ID, err)
		d.setLastError(err)
	}
	return err
}

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	tmpFile, err := os.CreateTemp("", "baidu-netdisk-upload-*")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	written, err := io.Copy(tmpFile, body)
	if err != nil {
		tmpFile.Close()
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload temp write: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return drive.Entry{}, err
	}
	if written != size {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload size mismatch: wrote %d, expected %d", written, size)
	}
	return d.PutFile(ctx, parentID, name, size, tmpPath)
}

func (d *Driver) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (drive.Entry, error) {
	if size < 1 {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: empty files are not allowed by baidu netdisk")
	}
	stat, err := os.Stat(localPath)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload stat: %w", err)
	}
	if stat.Size() != size {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload size mismatch: file has %d, expected %d", stat.Size(), size)
	}
	parentPath := d.resolvePath(parentID)
	remotePath := path.Join(parentPath, name)
	blockList, contentMD5, sliceMD5, err := uploadHashes(localPath, size)
	if err != nil {
		return drive.Entry{}, err
	}
	blockListJSON, err := json.Marshal(blockList)
	if err != nil {
		return drive.Entry{}, err
	}
	var pre precreateResp
	if err := d.precreate(ctx, remotePath, size, string(blockListJSON), contentMD5, sliceMD5, &pre); err != nil {
		err = fmt.Errorf("baidu_netdisk: upload precreate: %w", err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	if pre.ReturnType == 2 {
		return pre.File.entry(parentPath), nil
	}
	if pre.UploadID == "" {
		return drive.Entry{}, fmt.Errorf("baidu_netdisk: upload precreate returned empty uploadid")
	}
	if err := d.uploadParts(ctx, localPath, remotePath, name, size, pre.UploadID, pre.BlockList); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	var created createResp
	if err := d.createFile(ctx, remotePath, size, pre.UploadID, string(blockListJSON), &created); err != nil {
		err = fmt.Errorf("baidu_netdisk: upload create: %w", err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	entry := drive.Entry{ID: remotePath, ParentID: parentPath, Name: name, Size: size}
	if created.File.Path != "" {
		entry = created.File.entry(parentPath)
	} else if created.Path != "" {
		entry.ID = created.Path
	}
	if created.FsID > 0 {
		entry.Extra = map[string]any{"fs_id": strconv.FormatInt(created.FsID, 10)}
	}
	return entry, nil
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

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	if p == "" || p == "/" {
		return d.rootPath, nil
	}
	return normalizeDir(path.Join(d.rootPath, strings.Trim(p, "/"))), nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	d.lastErrorMu.Lock()
	lastError := d.lastError
	d.lastErrorMu.Unlock()
	return drive.DebugSnapshot{
		Driver:      "baidu_netdisk",
		Health:      "unknown",
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"root_path":      d.rootPath,
			"order_by":       d.orderBy,
			"order_desc":     d.orderDesc,
			"use_online_api": d.useOnlineAPI,
			"upload_api":     d.uploadAPI,
			"token_source":   d.tokenSource,
		},
		Extra: map[string]any{"last_error": lastError, "token_updated_at": d.tokenUpdated},
	}, nil
}

func (d *Driver) HealthCheck(ctx context.Context) drive.HealthStatus {
	start := time.Now()
	status := drive.HealthStatus{Driver: "baidu_netdisk", CheckedAt: start}
	_, err := d.Space(ctx)
	status.Latency = time.Since(start).String()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.OK = true
	return status
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
	return drive.Entry{}, fmt.Errorf("path not found")
}

func (d *Driver) resolvePath(id string) string {
	if id == "" || id == "/" || id == "0" {
		return d.rootPath
	}
	return normalizeDir(id)
}

func (d *Driver) downloadURL(ctx context.Context, entry drive.Entry) (string, error) {
	if cached, ok := d.downloadCache.Load(entry.ID); ok {
		if item, ok := cached.(cachedDownloadURL); ok && item.URL != "" && time.Now().Before(item.ExpiresAt) {
			return item.URL, nil
		}
		d.downloadCache.Delete(entry.ID)
	}
	fsID := entryFSID(entry)
	if fsID == "" {
		return "", fmt.Errorf("baidu_netdisk: missing fs_id for %q", entry.ID)
	}
	var resp downloadResp
	if err := d.get(ctx, "/xpan/multimedia", map[string]string{
		"method": "filemetas",
		"fsids":  "[" + fsID + "]",
		"dlink":  "1",
	}, &resp); err != nil {
		return "", fmt.Errorf("baidu_netdisk: download url: %w", err)
	}
	if len(resp.List) == 0 || resp.List[0].Dlink == "" {
		return "", fmt.Errorf("baidu_netdisk: download url is empty")
	}
	dlink := resp.List[0].Dlink
	if strings.Contains(dlink, "?") {
		dlink += "&access_token=" + url.QueryEscape(d.accessToken)
	} else {
		dlink += "?access_token=" + url.QueryEscape(d.accessToken)
	}
	redirectURL, err := d.resolveDownloadRedirect(ctx, dlink)
	if err != nil {
		return "", err
	}
	d.downloadCache.Store(entry.ID, cachedDownloadURL{URL: redirectURL, ExpiresAt: time.Now().Add(defaultDownloadTTL - defaultTokenSkew)})
	return redirectURL, nil
}

func (d *Driver) resolveDownloadRedirect(ctx context.Context, u string) (string, error) {
	client := *d.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", d.downloadUA)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("baidu_netdisk: download redirect: %w", err)
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if location == "" {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return u, nil
		}
		return "", fmt.Errorf("baidu_netdisk: download redirect status %d", resp.StatusCode)
	}
	if parsed, err := url.Parse(location); err == nil && !parsed.IsAbs() {
		location = resp.Request.URL.ResolveReference(parsed).String()
	}
	return location, nil
}

func (d *Driver) get(ctx context.Context, pathname string, params map[string]string, out any) error {
	return d.request(ctx, http.MethodGet, d.apiBaseURL+pathname, params, nil, out)
}

func (d *Driver) postForm(ctx context.Context, pathname string, params, form map[string]string, out any) error {
	return d.request(ctx, http.MethodPost, d.apiBaseURL+pathname, params, form, out)
}

func (d *Driver) request(ctx context.Context, method, rawURL string, params, form map[string]string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := d.ensureToken(ctx); err != nil {
			return err
		}
		err := d.doRequest(ctx, method, rawURL, params, form, out)
		if tokenExpired(err) {
			if refreshErr := d.refresh(ctx); refreshErr != nil {
				return refreshErr
			}
			lastErr = err
			continue
		}
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return nil
	}
	return lastErr
}

func (d *Driver) doRequest(ctx context.Context, method, rawURL string, params, form map[string]string, out any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	query := u.Query()
	query.Set("access_token", d.accessToken)
	for key, value := range params {
		query.Set(key, value)
	}
	u.RawQuery = query.Encode()
	var body io.Reader
	if len(form) > 0 {
		values := url.Values{}
		for key, value := range form {
			values.Set(key, value)
		}
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	if len(form) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d: %s", resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	if errno, errmsg := responseErrno(data); errno != 0 {
		return apiError{errno: errno, message: errmsg}
	}
	return nil
}

func (d *Driver) create(ctx context.Context, p string, size int64, isDir int, out any) error {
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "create"}, map[string]string{
		"path":  p,
		"size":  strconv.FormatInt(size, 10),
		"isdir": strconv.Itoa(isDir),
		"rtype": "3",
	}, out)
}

func (d *Driver) createFile(ctx context.Context, p string, size int64, uploadID, blockList string, out any) error {
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "create"}, map[string]string{
		"path":       p,
		"size":       strconv.FormatInt(size, 10),
		"isdir":      "0",
		"rtype":      "3",
		"uploadid":   uploadID,
		"block_list": blockList,
	}, out)
}

func (d *Driver) precreate(ctx context.Context, p string, size int64, blockList, contentMD5, sliceMD5 string, out any) error {
	return d.postForm(ctx, "/xpan/file", map[string]string{"method": "precreate"}, map[string]string{
		"path":        p,
		"size":        strconv.FormatInt(size, 10),
		"isdir":       "0",
		"autoinit":    "1",
		"rtype":       "3",
		"block_list":  blockList,
		"content-md5": contentMD5,
		"slice-md5":   sliceMD5,
	}, out)
}

func (d *Driver) uploadParts(ctx context.Context, localPath, remotePath, name string, size int64, uploadID string, blockList []int) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("baidu_netdisk: upload open: %w", err)
	}
	defer file.Close()
	partSize := uploadPartSize(size)
	for _, partSeq := range blockList {
		if partSeq < 0 {
			continue
		}
		offset := int64(partSeq) * partSize
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		if length < 0 {
			length = 0
		}
		section := io.NewSectionReader(file, offset, length)
		if err := d.uploadSlice(ctx, remotePath, name, uploadID, partSeq, section); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) uploadSlice(ctx context.Context, remotePath, name, uploadID string, partSeq int, section *io.SectionReader) error {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, d.limiter.LimitUpload(ctx, section)); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	u, err := url.Parse(d.uploadAPI + "/rest/2.0/pcs/superfile2")
	if err != nil {
		return err
	}
	query := u.Query()
	query.Set("method", "upload")
	query.Set("access_token", d.accessToken)
	query.Set("type", "tmpfile")
	query.Set("path", remotePath)
	query.Set("uploadid", uploadID)
	query.Set("partseq", strconv.Itoa(partSeq))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = int64(body.Len())
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("baidu_netdisk: upload part %d: %w", partSeq, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("baidu_netdisk: upload part %d status %d: %s", partSeq, resp.StatusCode, string(data))
	}
	var uploadResp uploadSliceResp
	if err := json.Unmarshal(data, &uploadResp); err == nil {
		if uploadResp.ErrorCode != 0 {
			return fmt.Errorf("baidu_netdisk: upload part %d error_code %d: %s", partSeq, uploadResp.ErrorCode, uploadResp.ErrorMsg)
		}
		if uploadResp.Errno != 0 {
			return fmt.Errorf("baidu_netdisk: upload part %d errno %d: %s", partSeq, uploadResp.Errno, uploadResp.Errmsg)
		}
	}
	return nil
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
	if state.RefreshToken != "" {
		d.refreshToken = state.RefreshToken
		d.tokenSource = "state"
	}
	if state.AccessToken != "" {
		d.accessToken = state.AccessToken
	}
	if !state.ExpiresAt.IsZero() {
		d.tokenExpires = state.ExpiresAt
	}
	d.tokenUpdated = state.UpdatedAt
}

func (d *Driver) saveTokenState() error {
	if d.stateStore == nil {
		return nil
	}
	return d.stateStore.SaveJSON("baidu_netdisk_token.json", tokenState{
		AccessToken:  d.accessToken,
		RefreshToken: d.refreshToken,
		ExpiresAt:    d.tokenExpires,
		UpdatedAt:    d.tokenUpdated,
	})
}

func (d *Driver) requestToken(ctx context.Context, method, rawURL string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d: %s", resp.StatusCode, string(data))
	}
	return json.Unmarshal(data, out)
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

func entryFSID(entry drive.Entry) string {
	if extra, ok := entry.Extra.(map[string]any); ok {
		switch v := extra["fs_id"].(type) {
		case string:
			return v
		case int64:
			return strconv.FormatInt(v, 10)
		case int:
			return strconv.Itoa(v)
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

func uploadHashes(localPath string, size int64) ([]string, string, string, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("baidu_netdisk: upload hash open: %w", err)
	}
	defer file.Close()
	partSize := uploadPartSize(size)
	partCount := int((size + partSize - 1) / partSize)
	blockList := make([]string, 0, partCount)
	fileHash := md5.New()
	firstSliceHash := md5.New()
	firstRemaining := int64(firstSliceMD5Size)
	buf := make([]byte, 256*1024)
	for part := 0; part < partCount; part++ {
		partHash := md5.New()
		remaining := partSize
		if part == partCount-1 {
			remaining = size - int64(part)*partSize
		}
		for remaining > 0 {
			nr := int64(len(buf))
			if remaining < nr {
				nr = remaining
			}
			n, err := io.ReadFull(file, buf[:nr])
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, "", "", fmt.Errorf("baidu_netdisk: upload hash read: %w", err)
			}
			if n > 0 {
				chunk := buf[:n]
				fileHash.Write(chunk)
				partHash.Write(chunk)
				if firstRemaining > 0 {
					firstN := int64(n)
					if firstRemaining < firstN {
						firstN = firstRemaining
					}
					firstSliceHash.Write(chunk[:firstN])
					firstRemaining -= firstN
				}
				remaining -= int64(n)
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
		}
		blockList = append(blockList, hex.EncodeToString(partHash.Sum(nil)))
	}
	return blockList, hex.EncodeToString(fileHash.Sum(nil)), hex.EncodeToString(firstSliceHash.Sum(nil)), nil
}

func uploadPartSize(size int64) int64 {
	partSize := int64(defaultUploadPart)
	if size > int64(maxUploadParts)*partSize {
		partSize = (size + int64(maxUploadParts) - 1) / int64(maxUploadParts)
	}
	return partSize
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
