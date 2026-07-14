package aliyundrive

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/driver/traceutil"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const (
	defaultRootID         = "root"
	defaultUploadPartSize = 10 << 20
)

type Driver struct {
	drive.UnsupportedOperations
	cl                 *client
	urlCache           sync.Map
	rootID             string
	rootPath           string
	driveID            string
	userID             string
	orderBy            string
	orderDirection     string
	partSize           int64
	limiter            *drive.BandwidthLimiter
	stateStore         drive.StateStore
	tokenSource        string
	tokenUpdated       time.Time
	debugMu            sync.Mutex
	lastError          string
	instantUploadCount int64
}

type cachedDownloadURL struct {
	URL       string
	ExpiresAt time.Time
}

type tokenState struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

func init() {
	drive.Register("aliyundrive", func(params drive.Params) (drive.Driver, error) {
		refreshToken := params["refresh_token"]
		if refreshToken == "" {
			return nil, fmt.Errorf("aliyundrive: missing refresh_token")
		}
		driveID := params["drive_id"]
		if driveID == "" {
			return nil, fmt.Errorf("aliyundrive: missing drive_id")
		}
		return New(Options{
			RefreshToken:   refreshToken,
			DriveID:        driveID,
			RootPath:       params["root_path"],
			APIBaseURL:     params["api_base_url"],
			AuthURL:        params["auth_url"],
			OrderBy:        params["order_by"],
			OrderDirection: params["order_direction"],
		}), nil
	},
		drive.ParamDef{
			Name:        "refresh_token",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "Aliyun Drive refresh token for OAuth authentication",
			Example:     "your-refresh-token",
		},
		drive.ParamDef{
			Name:        "drive_id",
			Type:        "string",
			Required:    true,
			Description: "Aliyun Drive ID",
			Example:     "your-drive-id",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "api_base_url",
			Type:        "string",
			Description: "Custom API base URL",
			Example:     "https://openapi.alipan.com",
		},
		drive.ParamDef{
			Name:        "auth_url",
			Type:        "string",
			Description: "Custom OAuth token URL",
			Example:     "https://openapi.alipan.com/oauth/authorize",
		},
		drive.ParamDef{
			Name:        "order_by",
			Type:        "string",
			Description: "File listing sort field",
			Example:     "name",
		},
		drive.ParamDef{
			Name:        "order_direction",
			Type:        "string",
			Description: "Sort direction (ASC or DESC)",
			Example:     "ASC",
		},
	)
}

type Options struct {
	RefreshToken   string
	DriveID        string
	RootID         string
	RootPath       string
	APIBaseURL     string
	AuthURL        string
	OrderBy        string
	OrderDirection string
}

func New(opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = defaultRootID
	}
	orderBy := opts.OrderBy
	if orderBy == "" {
		orderBy = "updated_at"
	}
	orderDirection := strings.ToUpper(opts.OrderDirection)
	if orderDirection == "" {
		orderDirection = "DESC"
	}
	d := &Driver{
		cl:             newClient(opts.RefreshToken, clientOptions{APIBaseURL: opts.APIBaseURL, AuthURL: opts.AuthURL}),
		driveID:        opts.DriveID,
		rootID:         rootID,
		rootPath:       opts.RootPath,
		orderBy:        orderBy,
		orderDirection: orderDirection,
		partSize:       defaultUploadPartSize,
		tokenSource:    "config",
	}
	d.cl.onRefresh = d.saveRefreshedToken
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadTokenState()
	if err := d.cl.refresh(ctx); err != nil {
		return err
	}
	var user userResp
	if err := d.cl.request(ctx, http.MethodPost, "/v2/user/get", map[string]any{}, &user); err != nil {
		return fmt.Errorf("aliyundrive: user get: %w", err)
	}
	d.userID = user.UserID
	if err := d.cl.configureDevice(user.UserID); err != nil {
		return err
	}
	if d.rootPath != "" && d.rootPath != "/" {
		rootID, err := d.ResolvePath(ctx, d.rootPath)
		if err != nil {
			d.setLastError(err)
			return fmt.Errorf("aliyundrive: resolve root_path %q: %w", d.rootPath, err)
		}
		d.rootID = rootID
	}
	if err := d.validateRoot(ctx); err != nil {
		d.setLastError(err)
		return err
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

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashSHA1}
}

func (d *Driver) sourceSHA1(source drive.ReadOnlyFileSource) (string, bool) {
	sum, ok := drive.SourceHash(source, drive.HashSHA1)
	if !ok || len(sum) != sha1.Size {
		return "", false
	}
	return hex.EncodeToString(sum), true
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolveID(parentID)
	var entries []drive.Entry
	marker := ""
	for {
		var resp listResp
		body := map[string]any{
			"drive_id":                d.driveID,
			"fields":                  "*",
			"image_thumbnail_process": "image/resize,w_400/format,jpeg",
			"image_url_process":       "image/resize,w_1920/format,jpeg",
			"limit":                   200,
			"marker":                  marker,
			"order_by":                d.orderBy,
			"order_direction":         d.orderDirection,
			"parent_file_id":          parentID,
			"video_thumbnail_process": "video/snapshot,t_0,f_jpg,ar_auto,w_300",
			"url_expire_sec":          14400,
		}
		if err := d.cl.request(ctx, http.MethodPost, "/v2/file/list", body, &resp); err != nil {
			err = fmt.Errorf("aliyundrive: list drive_id=%q parent_file_id=%q: %w", d.driveID, parentID, err)
			d.setLastError(err)
			return nil, err
		}
		for _, item := range resp.Items {
			if item.FileID == "" {
				continue
			}
			entries = append(entries, item.entry(parentID))
		}
		if resp.NextMarker == "" {
			break
		}
		marker = resp.NextMarker
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, status, err := d.readWithDownloadURL(ctx, entry, offset, size, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		d.urlCache.Delete(entry.ID)
		if rc != nil {
			rc.Close()
		}
		rc, status, err = d.readWithDownloadURL(ctx, entry, offset, size, true)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK && status != http.StatusPartialContent {
		if rc != nil {
			rc.Close()
		}
		return nil, fmt.Errorf("aliyundrive: read status %d", status)
	}
	return d.limiter.LimitDownload(ctx, rc), nil
}

func (d *Driver) readWithDownloadURL(ctx context.Context, entry drive.Entry, offset, size int64, refresh bool) (io.ReadCloser, int, error) {
	url, err := d.downloadURL(ctx, entry.ID, refresh)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Referer", "https://www.alipan.com/")
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}
	start := time.Now()
	httpResp, err := d.cl.httpClient.Do(req)
	d.cl.recordTrace(ctx, drive.DebugTraceEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       traceutil.URL(req.URL),
		Status:    responseStatus(httpResp),
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"range": req.Header.Get("Range")},
		Error:     errorString(err),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("aliyundrive: read: %w", err)
	}
	return httpResp.Body, httpResp.StatusCode, nil
}

func (d *Driver) downloadURL(ctx context.Context, fileID string, refresh bool) (string, error) {
	if !refresh {
		if cached, ok := d.urlCache.Load(fileID); ok {
			item := cachedDownloadURL{}
			if typed, ok := cached.(cachedDownloadURL); ok {
				item = typed
			}
			if item.URL != "" && time.Now().Before(item.ExpiresAt) {
				return item.URL, nil
			}
			d.urlCache.Delete(fileID)
		}
	}
	const expireSec = 14400
	var resp downloadURLResp
	body := map[string]any{
		"drive_id":   d.driveID,
		"file_id":    fileID,
		"expire_sec": expireSec,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/file/get_download_url", body, &resp); err != nil {
		return "", fmt.Errorf("aliyundrive: download url: %w", err)
	}
	if resp.URL == "" {
		return "", fmt.Errorf("aliyundrive: download url is empty")
	}
	d.urlCache.Store(fileID, cachedDownloadURL{
		URL:       resp.URL,
		ExpiresAt: time.Now().Add((expireSec - 300) * time.Second),
	})
	return resp.URL, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	now := time.Now()
	parentID = d.resolveID(parentID)
	var resp createResp
	body := map[string]any{
		"check_name_mode": "refuse",
		"drive_id":        d.driveID,
		"name":            name,
		"parent_file_id":  parentID,
		"type":            "folder",
	}
	if err := d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &resp); err != nil {
		err = fmt.Errorf("aliyundrive: mkdir drive_id=%q parent_file_id=%q name=%q: %w", d.driveID, parentID, name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	return drive.Entry{ID: resp.FileID, ParentID: parentID, Name: name, IsDir: true, ModTime: responseModTime(resp.UpdatedAt, resp.CreatedAt, now)}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return d.batch(ctx, entry.ID, d.resolveID(dstParentID), "/file/move")
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	body := map[string]any{
		"check_name_mode": "refuse",
		"drive_id":        d.driveID,
		"file_id":         entry.ID,
		"name":            newName,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v3/file/update", body, nil); err != nil {
		return fmt.Errorf("aliyundrive: rename: %w", err)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	body := map[string]any{
		"drive_id": d.driveID,
		"file_id":  entry.ID,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/recyclebin/trash", body, nil); err != nil {
		return fmt.Errorf("aliyundrive: remove: %w", err)
	}
	return nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	now := time.Now()
	size := source.Size()
	parentID = d.resolveID(parentID)
	partCount := int(math.Ceil(float64(size) / float64(d.partSize)))
	if partCount == 0 {
		partCount = 1
	}
	partInfo := make([]map[string]int, 0, partCount)
	for i := 1; i <= partCount; i++ {
		partInfo = append(partInfo, map[string]int{"part_number": i})
	}
	var create createResp
	body := map[string]any{
		"check_name_mode": "overwrite",
		"drive_id":        d.driveID,
		"name":            name,
		"parent_file_id":  parentID,
		"part_info_list":  partInfo,
		"size":            size,
		"type":            "file",
	}
	// When source provides SHA1 (e.g. from crypt ContentDedupCrypt),
	// skip two-phase pre_hash negotiation: saves one API round trip
	// and avoids re-encrypting the full source on every Open().
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	if sha1sum, ok := d.sourceSHA1(source); ok {
		body["content_hash"] = sha1sum
		body["content_hash_name"] = "sha1"
		body["proof_version"] = "v1"
		proofCode, err := d.proofCode(ctx, source, size)
		if err != nil {
			return drive.Entry{}, err
		}
		body["proof_code"] = proofCode
	} else {
		if preHash, err := fileHeadSHA1(ctx, source, 1024); err == nil {
			body["pre_hash"] = preHash
		} else {
			body["content_hash_name"] = "none"
			body["proof_version"] = "v1"
		}
	}
	err := d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &create)
	var apiErr *apiStatusError
	if errors.As(err, &apiErr) && apiErr.code == "PreHashMatched" {
		delete(body, "pre_hash")
		instantFields, instantErr := d.instantUploadFields(ctx, source, size)
		if instantErr != nil {
			return drive.Entry{}, instantErr
		}
		for key, value := range instantFields {
			body[key] = value
		}
		err = d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &create)
	}
	if err != nil {
		return drive.Entry{}, fmt.Errorf("aliyundrive: upload create: %w", err)
	}
	if create.InstantUpload {
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
		d.debugMu.Lock()
		d.instantUploadCount++
		d.debugMu.Unlock()
		return drive.Entry{ID: create.FileID, ParentID: parentID, Name: name, Size: size, ModTime: responseModTime(create.UpdatedAt, create.CreatedAt, now)}, nil
	}
	if err := d.uploadParts(ctx, source, req.Progress, create.PartInfoList); err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	var complete completeResp
	completeBody := map[string]any{
		"drive_id":  d.driveID,
		"file_id":   create.FileID,
		"upload_id": create.UploadID,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/file/complete", completeBody, &complete); err != nil {
		return drive.Entry{}, fmt.Errorf("aliyundrive: upload complete: %w", err)
	}
	entry := drive.Entry{ID: create.FileID, ParentID: parentID, Name: name, Size: size, ModTime: responseModTime(complete.UpdatedAt, complete.CreatedAt, responseModTime(create.UpdatedAt, create.CreatedAt, now))}
	if complete.FileID != "" {
		entry.ID = complete.FileID
	}
	if complete.Name != "" {
		entry.Name = complete.Name
	}
	if complete.Size > 0 {
		entry.Size = complete.Size
	}
	return entry, nil
}

func responseModTime(updatedAt, createdAt *time.Time, fallback time.Time) time.Time {
	if updatedAt != nil {
		return *updatedAt
	}
	if createdAt != nil {
		return *createdAt
	}
	return fallback
}

func (d *Driver) instantUploadFields(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (map[string]any, error) {
	contentHash, err := fileSHA1(ctx, source)
	if err != nil {
		return nil, err
	}
	proofCode, err := d.proofCode(ctx, source, size)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content_hash":      contentHash,
		"content_hash_name": "sha1",
		"proof_code":        proofCode,
		"proof_version":     "v1",
	}, nil
}

func (d *Driver) proofCode(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if size <= 0 {
		return "", nil
	}
	accessToken := d.cl.currentAccessToken()
	sum := md5.Sum([]byte(accessToken))
	offsetSeed, ok := new(big.Int).SetString(hex.EncodeToString(sum[:])[:16], 16)
	if !ok {
		return "", fmt.Errorf("aliyundrive: calculate proof offset")
	}
	offset := new(big.Int).Mod(offsetSeed, big.NewInt(size)).Int64()
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: proof open: %w", err)
	}
	defer file.Close()
	buf := make([]byte, 8)
	n, err := file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("aliyundrive: proof read: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func fileHeadSHA1(ctx context.Context, source drive.ReadOnlyFileSource, limit int64) (string, error) {
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: pre hash open: %w", err)
	}
	defer file.Close()
	h := sha1.New()
	if _, err := io.CopyN(h, file, limit); err != nil && err != io.EOF {
		return "", fmt.Errorf("aliyundrive: pre hash read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA1(ctx context.Context, source drive.ReadOnlyFileSource) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		if len(sum) != sha1.Size {
			return "", fmt.Errorf("aliyundrive: source SHA-1 metadata has %d bytes, want %d", len(sum), sha1.Size)
		}
		return hex.EncodeToString(sum), nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: content hash open: %w", err)
	}
	defer file.Close()
	h := sha1.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("aliyundrive: content hash read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, parts []uploadPartInfo) error {
	file, err := source.Open(ctx)
	if err != nil {
		return fmt.Errorf("aliyundrive: upload open: %w", err)
	}
	defer file.Close()
	size := source.Size()
	for _, part := range parts {
		if part.UploadURL == "" {
			return fmt.Errorf("aliyundrive: upload part %d has empty url", part.PartNumber)
		}
		offset := int64(part.PartNumber-1) * d.partSize
		length := d.partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		if length < 0 {
			length = 0
		}
		reader := drive.NewUploadProgressReader(progress, io.NewSectionReader(file, offset, length))
		body := d.limiter.LimitUpload(ctx, reader)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.UploadURL, body)
		if err != nil {
			return err
		}
		req.ContentLength = length
		start := time.Now()
		resp, err := d.cl.httpClient.Do(req)
		d.cl.recordTrace(ctx, drive.DebugTraceEvent{
			Operation: "upload_part",
			Method:    req.Method,
			URL:       traceutil.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"part_number": part.PartNumber, "bytes": length},
			Error:     errorString(err),
		})
		if err != nil {
			return fmt.Errorf("aliyundrive: upload part %d: %w", part.PartNumber, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("aliyundrive: upload part %d status %d", part.PartNumber, resp.StatusCode)
		}
	}
	return nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	var resp capacityResp
	if err := d.cl.request(ctx, http.MethodPost, "https://api.aliyundrive.com/adrive/v1/user/driveCapacityDetails", map[string]any{}, &resp); err != nil {
		return drive.Space{}, fmt.Errorf("aliyundrive: space: %w", err)
	}
	return drive.Space{Total: resp.DriveTotalSize, Free: resp.DriveTotalSize - resp.DriveUsedSize}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return d.rootID, nil
	}
	current := d.rootID
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, current)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment && entry.IsDir {
				current = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("aliyundrive: path not found: %s", filepath.Join("/", path))
		}
	}
	return current, nil
}

func (d *Driver) batch(ctx context.Context, srcID, dstID, path string) error {
	var resp batchResp
	body := map[string]any{
		"requests": []map[string]any{{
			"headers": map[string]string{"Content-Type": "application/json"},
			"method":  "POST",
			"id":      srcID,
			"body": map[string]any{
				"drive_id":          d.driveID,
				"file_id":           srcID,
				"to_drive_id":       d.driveID,
				"to_parent_file_id": dstID,
			},
			"url": path,
		}},
		"resource": "file",
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v3/batch", body, &resp); err != nil {
		err = fmt.Errorf("aliyundrive: batch %s drive_id=%q file_id=%q dst_parent_id=%q: %w", path, d.driveID, srcID, dstID, err)
		d.setLastError(err)
		return err
	}
	if len(resp.Responses) == 0 {
		err := fmt.Errorf("aliyundrive: batch %s returned no responses", path)
		d.setLastError(err)
		return err
	}
	item := resp.Responses[0]
	if item.Status >= 200 && item.Status < 300 {
		return nil
	}
	err := fmt.Errorf("aliyundrive: batch %s failed status=%d body=%s", path, item.Status, string(item.Body))
	d.setLastError(err)
	return err
}

func (d *Driver) validateRoot(ctx context.Context) error {
	if d.driveID == "" {
		return fmt.Errorf("aliyundrive: drive_id is required")
	}
	if d.rootID == "" {
		return fmt.Errorf("aliyundrive: root_path resolved to empty folder ID")
	}
	if _, err := d.List(ctx, d.rootID); err != nil {
		return fmt.Errorf("aliyundrive: validate root drive_id=%q root_path=%q resolved_id=%q: %w", d.driveID, d.rootPath, d.rootID, err)
	}
	return nil
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	health := "ok"
	if d.getLastError() != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "aliyundrive",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"drive_id":              d.driveID,
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
			"user_id":               d.userID,
			"order_by":              d.orderBy,
			"order_direction":       d.orderDirection,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.tokenSource,
			drive.DebugExtraCredentialUpdated:  d.tokenUpdated,
			drive.DebugExtraLastError:          d.getLastError(),
			drive.DebugExtraInstantUploadCount: d.instantUploadCount,
		},
	}, nil
}

func (d *Driver) DebugTrace(ctx context.Context, since time.Time) ([]drive.DebugTraceEvent, error) {
	return d.cl.debugTrace(since), nil
}

func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (d *Driver) setLastError(err error) {
	if err == nil {
		return
	}
	d.debugMu.Lock()
	d.lastError = err.Error()
	d.debugMu.Unlock()
}

func (d *Driver) getLastError() string {
	d.debugMu.Lock()
	defer d.debugMu.Unlock()
	return d.lastError
}

func (d *Driver) loadTokenState() {
	if d.stateStore == nil {
		return
	}
	var state tokenState
	err := d.stateStore.LoadJSON("aliyundrive_token.json", &state)
	if err != nil {
		if !drive.IsStateNotExist(err) {
			d.setLastError(fmt.Errorf("aliyundrive: load token state: %w", err))
		}
		return
	}
	if state.AccessToken != "" || state.RefreshToken != "" {
		d.cl.setTokens(state.AccessToken, state.RefreshToken)
		d.tokenSource = "state"
	}
	d.tokenUpdated = state.UpdatedAt
}

func (d *Driver) saveRefreshedToken(accessToken, refreshToken string) {
	d.tokenSource = "refresh"
	d.tokenUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	if err := d.stateStore.SaveJSON("aliyundrive_token.json", tokenState{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		UpdatedAt:    d.tokenUpdated,
	}); err != nil {
		d.setLastError(fmt.Errorf("aliyundrive: save token state: %w", err))
	}
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
