package drime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

const (
	defaultAPIBase   = "https://app.drime.cloud/api/v1"
	defaultChunkSize = int64(64 * 1024 * 1024)
	minChunkSize     = int64(5 * 1024 * 1024)
	maxUploadParts   = 10000
	smallUploadLimit = int64(5 * 1024 * 1024)
	maxListPages     = 200
	sessionFile      = "drime_upload_sessions.json"
	sessionMaxAge    = session.DefaultMaxAge
	sessionExpire    = 4 * time.Hour
)

func drimeParams() []drive.ParamDef {
	return []drive.ParamDef{
		{Name: "access_token", Type: "string", Required: true, Secret: true, Scope: drive.ParamScopeCredential, Description: "Drime personal access token", DescriptionI18n: map[string]string{"zh-CN": "Drime 个人访问令牌"}, Example: "your-access-token"},
		{Name: "workspace_id", Type: "int", Scope: drive.ParamScopeMount, Description: "Workspace ID; 0 is the personal space", DescriptionI18n: map[string]string{"zh-CN": "工作空间 ID；0 表示个人空间"}, Default: "0", Example: "1234"},
		{Name: "root_path", Type: "string", Scope: drive.ParamScopeMount, Description: "Directory path used as this mount root", DescriptionI18n: map[string]string{"zh-CN": "作为挂载根的目录路径"}, Default: "/", Example: "/qrypt"},
		{Name: "root_id", Type: "string", Scope: drive.ParamScopeMount, Description: "Pre-resolved folder ID; skips root_path resolution", DescriptionI18n: map[string]string{"zh-CN": "预解析的文件夹 ID；设置后跳过 root_path"}, Example: "1032808373"},
		{Name: "api_base_url", Type: "string", Scope: drive.ParamScopeBoth, Description: "Custom Drime API base URL", DescriptionI18n: map[string]string{"zh-CN": "自定义 Drime API 基础 URL"}, Default: defaultAPIBase, Example: defaultAPIBase},
		{Name: "chunk_size", Type: "int", Scope: drive.ParamScopeMount, Description: "Multipart upload chunk size in MiB (minimum 5)", DescriptionI18n: map[string]string{"zh-CN": "分片上传的块大小（MiB，最小 5）"}, Default: "64", Example: "64"},
		{Name: "upload_concurrency", Type: "int", Scope: drive.ParamScopeMount, Description: "Concurrent multipart part uploads", DescriptionI18n: map[string]string{"zh-CN": "分片上传并发数"}, Default: "4", Example: "4"},
		{Name: "instant_upload", Type: "bool", Scope: drive.ParamScopeMount, Description: "Reuse verified provider content instead of re-uploading", DescriptionI18n: map[string]string{"zh-CN": "校验通过后复用网盘已有内容，避免重复上传"}, Default: "true"},
		{Name: "presign_small", Type: "bool", Scope: drive.ParamScopeMount, Description: "Use presigned single-part uploads below 5 MiB", DescriptionI18n: map[string]string{"zh-CN": "小于 5 MiB 的文件使用预签名直传"}, Default: "false"},
		{Name: "limit_rate", Type: "int", Scope: drive.ParamScopeMount, Description: "Limit API requests per second (0 = unlimited)", DescriptionI18n: map[string]string{"zh-CN": "限制 API 请求速率（每秒 N 次，0 表示不限制）"}, Default: "0", Example: "10"},
		{Name: "hard_delete", Type: "bool", Scope: drive.ParamScopeMount, Description: "Delete permanently instead of moving to trash", DescriptionI18n: map[string]string{"zh-CN": "永久删除而不是移到回收站"}, Default: "false"},
	}
}

func init() {
	drive.Register("drime", func(params drive.Params) (drive.Driver, error) {
		token := strings.TrimSpace(params["access_token"])
		if token == "" {
			return nil, fmt.Errorf("drime: missing access_token")
		}
		workspaceID := strings.TrimSpace(params["workspace_id"])
		if workspaceID == "" {
			workspaceID = "0"
		}
		if _, err := strconv.ParseInt(workspaceID, 10, 64); err != nil {
			return nil, fmt.Errorf("drime: invalid workspace_id %q: %w", workspaceID, drive.ErrInvalidInput)
		}
		chunkSize := defaultChunkSize
		if raw := strings.TrimSpace(params["chunk_size"]); raw != "" {
			mib, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || mib < 5 {
				return nil, fmt.Errorf("drime: chunk_size must be an integer >= 5 MiB: %w", drive.ErrInvalidInput)
			}
			chunkSize = mib * 1024 * 1024
		}
		concurrency := 4
		if raw := strings.TrimSpace(params["upload_concurrency"]); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 32 {
				return nil, fmt.Errorf("drime: upload_concurrency must be between 1 and 32: %w", drive.ErrInvalidInput)
			}
			concurrency = n
		}
		limitRate := 0
		if raw := strings.TrimSpace(params["limit_rate"]); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("drime: limit_rate must be a non-negative integer: %w", drive.ErrInvalidInput)
			}
			limitRate = n
		}
		instantUpload := true
		if raw := strings.TrimSpace(params["instant_upload"]); raw != "" {
			instantUpload = !strings.EqualFold(raw, "false")
		}
		return New(Options{
			AccessToken:       token,
			WorkspaceID:       workspaceID,
			RootPath:          params["root_path"],
			RootID:            params["root_id"],
			APIBaseURL:        params["api_base_url"],
			ChunkSize:         chunkSize,
			UploadConcurrency: concurrency,
			InstantUpload:     instantUpload,
			PresignSmall:      strings.EqualFold(params["presign_small"], "true"),
			LimitRate:         limitRate,
			HardDelete:        strings.EqualFold(params["hard_delete"], "true"),
		}), nil
	}, drimeParams()...)
}

type Options struct {
	AccessToken       string
	WorkspaceID       string
	RootPath          string
	RootID            string
	APIBaseURL        string
	ChunkSize         int64
	UploadConcurrency int
	InstantUpload     bool
	PresignSmall      bool
	LimitRate         int
	HardDelete        bool
}

type Driver struct {
	drive.UnsupportedOperations

	cl                *client
	workspaceID       string
	rootPath          string
	rootID            string
	chunkSize         int64
	uploadConcurrency int
	instantUpload     bool
	presignSmall      bool
	hardDelete        bool
	instantUploads    atomic.Int64

	stateStore    drive.StateStore
	sessions      *session.Index
	sessionMu     sync.Mutex
	sessionCancel context.CancelFunc

	limiter *drive.BandwidthLimiter

	lastErrMu sync.Mutex
	lastErr   string
}

func New(opts Options) *Driver {
	workspaceID := strings.TrimSpace(opts.WorkspaceID)
	if workspaceID == "" {
		workspaceID = "0"
	}
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if chunkSize < minChunkSize {
		chunkSize = minChunkSize
	}
	concurrency := opts.UploadConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 32 {
		concurrency = 32
	}
	rootPath := strings.TrimSpace(opts.RootPath)
	if rootPath == "" {
		rootPath = "/"
	}
	return &Driver{
		workspaceID:       workspaceID,
		rootPath:          rootPath,
		rootID:            strings.TrimSpace(opts.RootID),
		chunkSize:         chunkSize,
		uploadConcurrency: concurrency,
		instantUpload:     opts.InstantUpload,
		presignSmall:      opts.PresignSmall,
		hardDelete:        opts.HardDelete,
		cl:                mustClient(opts),
	}
}

func mustClient(opts Options) *client {
	c, err := newClient(opts)
	if err != nil {
		// newClient only rejects malformed base URLs, which the registered
		// factory validates through config; fall back to the public endpoint so
		// a hand-constructed Driver in tests still has a usable client.
		return &client{
			base:         defaultAPIBase,
			downloadBase: "https://app.drime.cloud/",
			workspaceID:  opts.WorkspaceID,
			token:        opts.AccessToken,
		}
	}
	c.workspaceID = opts.WorkspaceID
	return c
}

func (d *Driver) Init(ctx context.Context) error {
	var me apiUserResponse
	if _, err := d.cl.get(ctx, "/users/me", &me); err != nil {
		d.setLastError(err)
		return fmt.Errorf("drime: validate access_token: %w", err)
	}
	wsID, err := strconv.ParseInt(d.workspaceID, 10, 64)
	if err != nil || wsID < 0 {
		return fmt.Errorf("drime: invalid workspace_id %q: %w", d.workspaceID, drive.ErrInvalidInput)
	}
	if wsID != 0 {
		var list struct {
			Workspaces []struct {
				ID int64 `json:"id"`
			} `json:"workspaces"`
		}
		if _, err := d.cl.get(ctx, "/me/workspaces", &list); err != nil {
			d.setLastError(err)
			return fmt.Errorf("drime: list workspaces: %w", err)
		}
		found := false
		for _, ws := range list.Workspaces {
			if ws.ID == wsID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("drime: workspace %d is not visible to this token: %w", wsID, drive.ErrAuth)
		}
	}

	if d.rootID != "" {
		if err := d.verifyRoot(ctx, d.rootID); err != nil {
			return err
		}
		return nil
	}
	if clean, err := cleanMountPath(d.rootPath); err != nil {
		return fmt.Errorf("drime: invalid root_path %q: %w", d.rootPath, err)
	} else if clean == "/" {
		d.rootID = ""
		return nil
	}
	resolved, err := d.resolvePathFrom(ctx, "", d.rootPath)
	if err != nil {
		return fmt.Errorf("drime: resolve root_path %q: %w", d.rootPath, err)
	}
	if err := d.verifyRoot(ctx, resolved); err != nil {
		return err
	}
	d.rootID = resolved
	return nil
}

func (d *Driver) verifyRoot(ctx context.Context, id string) error {
	var resp struct {
		FileEntry *apiEntry `json:"fileEntry"`
	}
	if _, err := d.cl.get(ctx, "/file-entries/"+url.PathEscape(id)+"/model?workspaceId="+url.QueryEscape(d.workspaceID), &resp); err != nil {
		return fmt.Errorf("drime: resolve root %q: %w", id, err)
	}
	if resp.FileEntry == nil {
		return fmt.Errorf("drime: root %q not found: %w", id, drive.ErrNotFound)
	}
	if !resp.FileEntry.isDir() {
		return fmt.Errorf("drime: root %q is not a directory: %w", id, drive.ErrInvalidInput)
	}
	if entry := resp.FileEntry.entry(""); !entryIsWorkspace(entry, d.workspaceID) {
		return fmt.Errorf("drime: root %q belongs to another workspace: %w", id, drive.ErrAuth)
	}
	return nil
}

func entryIsWorkspace(entry drive.Entry, workspaceID string) bool {
	raw, ok := drive.RawEntryExtra(entry.Extra).(map[string]any)
	if !ok {
		return true
	}
	value, ok := raw["workspace_id"]
	if !ok {
		return true
	}
	switch v := value.(type) {
	case int64:
		return strconv.FormatInt(v, 10) == workspaceID
	case int:
		return strconv.Itoa(v) == workspaceID
	case float64:
		return strconv.FormatInt(int64(v), 10) == workspaceID
	case string:
		return v == workspaceID
	}
	return true
}

func (d *Driver) Drop(ctx context.Context) error {
	d.sessionMu.Lock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessionMu.Unlock()
	if d.sessions != nil {
		_ = d.sessions.Flush()
	}
	d.cl.http.CloseIdleConnections()
	return nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp apiSpaceResponse
	_, err := d.cl.get(ctx, "/user/space-usage?workspaceId="+url.QueryEscape(d.workspaceID), &resp)
	if err != nil {
		if errors.Is(err, drive.ErrNotFound) {
			return drive.Space{}, drive.ErrSpaceUnsupported
		}
		d.setLastError(err)
		return drive.Space{}, fmt.Errorf("drime: space: %w", err)
	}
	return drive.Space{Total: resp.Used + resp.Available, Free: resp.Available}, nil
}

func (d *Driver) resolveID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	clean, err := cleanMountPath(p)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return d.rootID, nil
	}
	return d.resolvePathFrom(ctx, d.rootID, clean)
}

func (d *Driver) resolvePathFrom(ctx context.Context, start, p string) (string, error) {
	clean, err := cleanMountPath(p)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return start, nil
	}
	parent := start
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	for _, part := range parts {
		child, found, err := d.lookupChild(ctx, parent, part)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fmt.Errorf("drime: path %q: %w", p, drive.ErrNotFound)
		}
		if !child.IsDir {
			return "", fmt.Errorf("drime: path %q is not a directory: %w", p, drive.ErrInvalidInput)
		}
		parent = child.ID
	}
	return parent, nil
}

func cleanMountPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" || p == "." {
		return "/", nil
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("path escapes mount root: %w", drive.ErrInvalidInput)
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

func (d *Driver) chunkSizeFor(size int64) int64 {
	chunk := d.chunkSize
	if chunk < minChunkSize {
		chunk = minChunkSize
	}
	if size > 0 {
		needed := (size + maxUploadParts - 1) / maxUploadParts
		needed = ((needed + minChunkSize - 1) / minChunkSize) * minChunkSize
		if needed > chunk {
			chunk = needed
		}
	}
	const maxChunk = int64(5 * 1024 * 1024 * 1024)
	if chunk > maxChunk {
		chunk = maxChunk
	}
	return chunk
}

func (d *Driver) setLastError(err error) {
	if err == nil {
		return
	}
	d.lastErrMu.Lock()
	d.lastErr = drive.Snippet([]byte(err.Error()))
	d.lastErrMu.Unlock()
}

func (d *Driver) getLastError() string {
	d.lastErrMu.Lock()
	defer d.lastErrMu.Unlock()
	return d.lastErr
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.sessionMu.Lock()
	defer d.sessionMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, sessionFile, session.IndexOptions{
		OnError: func(err error) {
			d.setLastError(fmt.Errorf("drime: upload session state: %w", err))
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, sessionExpire, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(sessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

func (d *Driver) reclaimUploadSession(binding session.Session) error {
	var token uploadSessionToken
	if err := json.Unmarshal(binding.Token, &token); err != nil || token.Key == "" || token.UploadID == "" {
		return nil
	}
	_, err := d.cl.post(context.Background(), "/s3/multipart/abort", map[string]any{
		"key":      token.Key,
		"uploadId": token.UploadID,
	}, nil)
	if err != nil && !errors.Is(err, drive.ErrNotFound) {
		return err
	}
	return nil
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

var (
	_ drive.StateStoreInstaller     = (*Driver)(nil)
	_ drive.BandwidthLimitInstaller = (*Driver)(nil)
)
