package yun139

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httputil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"golang.org/x/sync/errgroup"
)

const maxUploadSize = 2 << 30

type partMeta struct {
	PartNumber int64 `json:"partNumber"`
	PartSize   int64 `json:"partSize"`
}

const (
	uploadPartConcurrency = 1
	quotaSizeUnit         = util.MiB
)

type Driver struct {
	drive.UnsupportedOperations
	cl                  *client
	rootID              string
	rootPath            string
	limiter             *drive.BandwidthLimiter
	stateStore          drive.StateStore
	authSource          string
	authUpdated         time.Time
	configAuthorization string
	debugMu             sync.Mutex
	lastError           string
	instantUploadCount  int64

	// Upload session binding store：只保存 "内容键 → provider 上传句柄 +
	// 本地确认位图"。139 无分片进度查询接口，能跳过已确认分片靠的是
	// 本地确认记录（TouchWith 节流落盘，幂等重传兜底）。
	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc
}

type authState struct {
	Authorization string `json:"authorization,omitempty"`
	// ConfigAuthorization records the config authorization that produced this
	// state, so a refreshed value (state newer than config) can be distinguished
	// from a user-swapped config token (config newer than state).
	ConfigAuthorization string    `json:"config_authorization,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

const yun139SessionFile = "yun139_upload_sessions.json"
const yun139SessionMaxAge = 24 * time.Hour
const yun139SessionExpiryEvery = time.Hour

func init() {
	drive.Register("yun139", func(params drive.Params) (drive.Driver, error) {
		auth := params["authorization"]
		if auth == "" {
			return nil, fmt.Errorf("139: missing authorization")
		}
		return New(auth, params["root_path"], params["root_id"]), nil
	},
		drive.ParamDef{
			Name:        "authorization",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "139 cloud drive authorization token",
			Example:     "your-authorization-token",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Virtual root path, resolved to the provider folder ID at startup",
			Default:     "/",
			Example:     "/qrypt",
		},
		drive.ParamDef{
			Name:        "root_id",
			Type:        "string",
			Description: "Pre-resolved folder ID (skips root_path resolution)",
			Example:     "FtozqWiFB1yWOWUGc9oNCf6M0h5fRwcQl",
		},
	)
}

func New(authorization, rootPath, rootID string) *Driver {
	d := &Driver{
		cl:                  newClient(authorization),
		rootPath:            rootPath,
		rootID:              rootID,
		configAuthorization: authorization,
		authSource:          "config",
	}
	d.cl.onAuthorizationUpdate = d.saveUpdatedAuthorization
	return d
}

func (d *Driver) Init(ctx context.Context) error {
	d.loadAuthState()
	if _, _, err := d.cl.decodeAuth(); err != nil {
		return fmt.Errorf("139: invalid authorization: %w", err)
	}
	if err := d.cl.refreshTokenIfNeeded(ctx); err != nil {
		return fmt.Errorf("139: refresh authorization: %w", err)
	}
	if err := d.cl.ensurePersonalCloudHost(); err != nil {
		return fmt.Errorf("139: resolve host: %w", err)
	}
	if d.rootID == "" {
		d.rootID = "/"
		if d.rootPath != "" && d.rootPath != "/" {
			rootID, err := d.resolvePathFrom(ctx, d.rootID, d.rootPath)
			if err != nil {
				return fmt.Errorf("139: resolve root_path %q: %w", d.rootPath, err)
			}
			d.rootID = rootID
		}
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error {
	d.sessionStoreMu.Lock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessionStoreMu.Unlock()
	if d.sessions != nil {
		_ = d.sessions.Flush()
	}
	return nil
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.installSessionIndex(store)
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashSHA256}
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) loadAuthState() {
	if d.stateStore == nil {
		return
	}
	var state authState
	err := d.stateStore.LoadJSON("yun139_auth.json", &state)
	if err != nil {
		return
	}
	// The state wins when it was derived from the current config authorization
	// (refresh) or when it predates the source marker. A config authorization
	// that differs from the one the state was derived from is an account switch.
	stateDerived := state.ConfigAuthorization == "" || state.ConfigAuthorization == d.configAuthorization
	if state.Authorization != "" && stateDerived {
		d.cl.setAuthorization(state.Authorization)
		d.authSource = "state"
	}
	d.authUpdated = state.UpdatedAt
}

func (d *Driver) saveUpdatedAuthorization(authorization string) {
	if authorization == "" {
		return
	}
	d.authSource = "refresh"
	d.authUpdated = time.Now()
	if d.stateStore == nil {
		return
	}
	_ = d.stateStore.SaveJSON("yun139_auth.json", authState{
		Authorization:       authorization,
		ConfigAuthorization: d.configAuthorization,
		UpdatedAt:           d.authUpdated,
	})
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

func invalidResumedUploadSession(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "status 400") ||
		strings.Contains(msg, "status 404") ||
		strings.Contains(msg, "status 409") ||
		strings.Contains(msg, "status 410") ||
		strings.Contains(msg, "upload complete failed")
}

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	fileID := d.resolveID(parentID)

	var allEntries []drive.Entry
	cursor := ""
	for {
		data := map[string]interface{}{
			"imageThumbnailStyleList": []string{"Small", "Large"},
			"orderBy":                 "updated_at",
			"orderDirection":          "DESC",
			"pageInfo": map[string]interface{}{
				"pageCursor": cursor,
				"pageSize":   100,
			},
			"parentFileId": fileID,
		}
		var resp personalListResp
		err := d.cl.personalPost(ctx, "/file/list", data, &resp)
		if err != nil {
			return nil, fmt.Errorf("139: list: %w", err)
		}
		if !resp.Success {
			return nil, fmt.Errorf("139: list failed (code=%s): %s", resp.Code, resp.Message)
		}
		allEntries = append(allEntries, toEntries(resp.Data.Items)...)
		cursor = resp.Data.NextPageCursor
		if cursor == "" {
			break
		}
	}
	return allEntries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	url, err := d.getDownloadURL(ctx, entry.ID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("139: read: %w", err)
	}
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}

	start := time.Now()
	resp, err := d.cl.httpClient.Do(req)
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       driverutil.URL(req.URL),
		Status:    responseStatus(resp),
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"range": req.Header.Get("Range")},
		Error:     errorString(err),
	})
	if err != nil {
		return nil, fmt.Errorf("139: read download: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("139: read status %d", resp.StatusCode)
	}
	return d.limiter.LimitDownload(ctx, resp.Body), nil
}

func (d *Driver) getDownloadURL(ctx context.Context, fileID string) (string, error) {
	data := map[string]interface{}{"fileId": fileID}
	var resp downloadResp
	err := d.cl.personalPost(ctx, "/file/getDownloadUrl", data, &resp)
	if err != nil {
		return "", fmt.Errorf("139: download url: %w", err)
	}
	if !resp.Success {
		err := fmt.Errorf("139: download url failed (code=%s): %s", resp.Code, resp.Message)
		if resp.Code == "04000010" || strings.Contains(resp.Message, "资源不存在") {
			return "", fmt.Errorf("%w: %v", drive.ErrNotFound, err)
		}
		return "", err
	}
	if resp.Data.CDNURL != "" {
		return resp.Data.CDNURL, nil
	}
	return resp.Data.URL, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	now := time.Now()
	fileID := d.resolveID(parentID)
	data := map[string]interface{}{
		"parentFileId": fileID,
		"name":         name,
		"description":  "",
		"type":         "folder",
	}
	var resp createResp
	err := d.cl.personalPost(ctx, "/file/create", data, &resp)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("139: mkdir: %w", err)
	}
	if !resp.Success {
		// Name collision — FUSE layer handles this by looking up existing dir.
		return drive.Entry{}, fmt.Errorf("139: mkdir failed (code=%s): %s", resp.Code, resp.Message)
	}
	return drive.Entry{ID: resp.Data.FileID, ParentID: fileID, Name: resp.Data.Name, IsDir: true, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	data := map[string]interface{}{
		"fileIds":        []string{d.resolveID(entry.ID)},
		"toParentFileId": d.resolveID(dstParentID),
	}
	var resp baseResp
	err := d.cl.personalPost(ctx, "/file/batchMove", data, &resp)
	if err != nil {
		return fmt.Errorf("139: move: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("139: move failed (code=%s): %s", resp.Code, resp.Message)
	}
	return nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	data := map[string]interface{}{
		"fileId":      d.resolveID(entry.ID),
		"name":        newName,
		"description": "",
	}
	var resp baseResp
	err := d.cl.personalPost(ctx, "/file/update", data, &resp)
	if err != nil {
		return fmt.Errorf("139: rename: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("139: rename failed (code=%s): %s", resp.Code, resp.Message)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	data := map[string]interface{}{
		"fileIds": []string{d.resolveID(entry.ID)},
	}
	var resp baseResp
	err := d.cl.personalPost(ctx, "/recyclebin/batchTrash", data, &resp)
	if err != nil {
		return fmt.Errorf("139: remove: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("139: remove failed (code=%s): %s", resp.Code, resp.Message)
	}
	return nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	data := map[string]interface{}{
		"commonAccountInfo": map[string]interface{}{
			"account":     d.cl.getAccount(),
			"accountType": 1,
		},
	}
	if userDomainID := d.cl.getUserDomainID(); userDomainID != "" {
		data["userDomainId"] = userDomainID
	}

	var resp quotaDetailResp
	if err := d.cl.userPost(ctx, "/user/disk/quota/detail", data, &resp); err != nil {
		err = fmt.Errorf("139: space: %w", err)
		d.setLastError(err)
		return drive.Space{}, err
	}
	if !resp.Success {
		err := fmt.Errorf("139: space failed (code=%s): %s", resp.Code, resp.Message)
		d.setLastError(err)
		return drive.Space{}, err
	}
	return drive.Space{
		Total: resp.Data.DiskSize * quotaSizeUnit,
		Free:  resp.Data.FreeDiskSize * quotaSizeUnit,
	}, nil
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	size := source.Size()
	if size > maxUploadSize {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("139: upload %s (%d bytes) exceeds max size (%d)", name, size, int64(maxUploadSize)))
	}
	return d.putSource(ctx, parentID, name, source, req.Progress)
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	health := "ok"
	d.debugMu.Lock()
	lastError := d.lastError
	instantUploadCount := d.instantUploadCount
	d.debugMu.Unlock()
	if lastError != "" {
		health = "degraded"
	}
	return drive.DebugSnapshot{
		Driver:      "yun139",
		Health:      health,
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			drive.DebugStatRootID:   d.rootID,
			drive.DebugStatRootPath: d.rootPath,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource:   d.authSource,
			drive.DebugExtraCredentialUpdated:  d.authUpdated,
			drive.DebugExtraLastError:          lastError,
			drive.DebugExtraInstantUploadCount: instantUploadCount,
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.cl.metricEvents(since), nil
}

func (d *Driver) putSource(ctx context.Context, parentID, name string, source drive.ReadOnlyFileSource, progress drive.UploadProgress) (drive.Entry, error) {
	size := source.Size()
	now := time.Now()
	fileID := d.resolveID(parentID)
	partSize := calcPartSize(size)

	drive.ReportUploadPhase(progress, drive.UploadPhaseHashing)
	sha256Hex, err := sourceSHA256Hex(ctx, source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := session.Identity{ParentID: fileID, Name: name, Size: size, Fingerprint: sha256Hex}.Key()
	resumedSession := false
	var token yun139Token
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(sessionKey); ok {
			if err := json.Unmarshal(binding.Token, &token); err == nil && token.FileID != "" && token.UploadID != "" {
				resumedSession = true
				if token.PartSize > 0 {
					partSize = token.PartSize
				}
			} else {
				// 无效绑定：作废重来。
				d.sessions.Delete(sessionKey)
			}
		}
	}
	partCount := size / partSize
	if size%partSize > 0 {
		partCount++
	}

	partInfos := make([]partMeta, partCount)
	for i := int64(0); i < partCount; i++ {
		start := i * partSize
		byteSize := size - start
		if byteSize > partSize {
			byteSize = partSize
		}
		partInfos[i] = partMeta{
			PartNumber: i + 1,
			PartSize:   byteSize,
		}
	}

	createData := map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"contentType":          "application/octet-stream",
		"parallelUpload":       false,
		"partInfos":            partInfos,
		"size":                 size,
		"parentFileId":         fileID,
		"name":                 name,
		"type":                 "file",
		"fileRenameMode":       "auto_rename",
	}
	var createResp personalUploadResp
	if resumedSession {
		createResp = token.createResp()
	} else {
		// 预留绑定：/file/create 是 provider 上传资源创建，先落盘再调用，
		// 崩溃只留下空句柄绑定（下次作废重来）。
		if d.sessions != nil {
			if raw, err := json.Marshal(yun139Token{PartSize: partSize}); err != nil {
				return drive.Entry{}, fmt.Errorf("139: encode upload session: %w", err)
			} else if err := d.sessions.Create(sessionKey, raw); err != nil {
				return drive.Entry{}, fmt.Errorf("139: persist upload session: %w", err)
			}
		}
		if err := d.cl.personalPost(ctx, "/file/create", createData, &createResp); err != nil {
			if d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, fmt.Errorf("139: upload create: %w", err)
		}
		if !createResp.Success {
			if d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, fmt.Errorf("139: upload create failed (code=%s): %s", createResp.Code, createResp.Message)
		}
	}

	logging.L.Debugf("[139] upload create: fileId=%s exist=%v instant=%v parts=%d uploadId=%s",
		createResp.Data.FileID, createResp.Data.Exist, createResp.Data.InstantUpload,
		len(createResp.Data.PartInfos), createResp.Data.UploadID)

	if createResp.Data.Exist || createResp.Data.InstantUpload {
		drive.ReportUploadPhase(progress, drive.UploadPhaseInstant)
		d.debugMu.Lock()
		d.instantUploadCount++
		d.debugMu.Unlock()
		if d.sessions != nil {
			d.sessions.Delete(sessionKey)
		}
		return drive.Entry{ID: createResp.Data.FileID, ParentID: fileID, Name: name, Size: size, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
	}

	if !resumedSession && d.sessions != nil {
		token = yun139Token{
			FileID:    createResp.Data.FileID,
			FileName:  createResp.Data.FileName,
			UploadID:  createResp.Data.UploadID,
			PartSize:  partSize,
			Confirmed: session.ConfirmedBitmap(size, partSize, nil),
		}
		if raw, err := json.Marshal(token); err != nil {
			return drive.Entry{}, fmt.Errorf("139: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			// 持句柄落盘失败：provider 会话无 abort 端点，靠服务端过期；
			// 下次尝试发现空句柄时视为作废重来。
			return drive.Entry{}, fmt.Errorf("139: persist upload session: %w", err)
		}
	}

	// 分片上传：已确认分片跳过（本地确认位图），上传 URL 每次现取。
	if len(partInfos) > 0 {
		skipParts := session.ConfirmedParts(token.Confirmed)
		if err := d.uploadParts(ctx, source, progress, createResp, partInfos, partSize, size, skipParts, func(partNumber int) {
			if d.sessions == nil || sessionKey == "" {
				return
			}
			// 确认记录在 Index 锁内原地更新、节流落盘（≤1 次/分钟）；
			// 崩溃最多丢一分钟确认，对应分片重传幂等覆盖，安全。闭包模式
			// 对并发的分片确认也安全。
			d.sessions.TouchWith(sessionKey, func(s *session.Session) {
				var tok yun139Token
				if err := json.Unmarshal(s.Token, &tok); err != nil || tok.FileID == "" || tok.UploadID == "" {
					return
				}
				tok.Confirmed = session.ConfirmedBitmap(size, partSize, tok.Confirmed)
				session.MarkConfirmed(tok.Confirmed, partNumber)
				if raw, err := json.Marshal(tok); err == nil {
					s.Token = raw
				}
			})
		}); err != nil {
			if d.sessions != nil && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, err
		}
	}

	drive.ReportUploadPhase(progress, drive.UploadPhaseCommitting)
	completeData := map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"fileId":               createResp.Data.FileID,
		"uploadId":             createResp.Data.UploadID,
	}
	logging.L.Debugf("[139] upload complete: fileId=%s uploadId=%s", createResp.Data.FileID, createResp.Data.UploadID)
	var completeResp baseResp
	if err := d.cl.personalPost(ctx, "/file/complete", completeData, &completeResp); err != nil {
		if d.sessions != nil && invalidResumedUploadSession(err) {
			d.sessions.Delete(sessionKey)
		}
		return drive.Entry{}, fmt.Errorf("139: upload complete: %w", err)
	}
	if !completeResp.Success {
		if d.sessions != nil {
			d.sessions.Delete(sessionKey)
		}
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("139: upload complete failed (code=%s): %s", completeResp.Code, completeResp.Message))
	}

	// Handle auto_rename conflict: server renamed our uploaded file because
	// a file with the same name already existed in the target directory.
	// The old file gets removed and our new file is renamed back to original.
	if createResp.Data.FileName != "" && createResp.Data.FileName != name {
		logging.L.Infof("[139] upload was renamed by server: %q -> %q (new id=%s)", name, createResp.Data.FileName, createResp.Data.FileID)

		// 1. Remove all stale duplicates with a different file ID
		entries, err := d.List(ctx, parentID)
		if err != nil {
			logging.L.Warnf("[139] failed to list files for conflict resolution: %v", err)
		} else {
			for _, e := range entries {
				if e.Name == name && !e.IsDir && e.ID != createResp.Data.FileID {
					logging.L.Infof("[139] removing duplicate file: name=%q id=%q (keeping new id=%q)", name, e.ID, createResp.Data.FileID)
					if err := d.Remove(ctx, e); err != nil {
						logging.L.Warnf("[139] failed to remove duplicate file id=%s: %v", e.ID, err)
					}
				}
			}
		}

		// 2. Rename our new file back to the original name using its stable
		// file ID (toEntry strips the suffix so the list name is ambiguous).
		if err := d.Rename(ctx, drive.Entry{ID: createResp.Data.FileID}, name); err != nil {
			logging.L.Warnf("[139] failed to rename new file id=%s back to %q: %v", createResp.Data.FileID, name, err)
			if d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{ID: createResp.Data.FileID, ParentID: fileID, Name: name, Size: size, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
		}
	}

	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return drive.Entry{ID: createResp.Data.FileID, ParentID: fileID, Name: name, Size: size, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
}

func sourceSHA256Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashSHA256); ok {
		if len(sum) != sha256.Size {
			return "", drive.NonRetryable(fmt.Errorf("139: source SHA-256 metadata has %d bytes, want %d", len(sum), sha256.Size))
		}
		return fmt.Sprintf("%X", sum), nil
	}
	hashFile, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("139: upload hash open: %w", err)
	}
	hasher := sha256.New()
	hashed, err := io.Copy(hasher, hashFile)
	closeErr := hashFile.Close()
	if err != nil {
		return "", fmt.Errorf("139: upload hash: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("139: upload hash close: %w", closeErr)
	}
	if hashed != size {
		return "", fmt.Errorf("139: upload size mismatch: hashed %d, expected %d", hashed, size)
	}
	return fmt.Sprintf("%X", hasher.Sum(nil)), nil
}

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, createResp personalUploadResp, partInfos []partMeta, partSize, size int64, completed map[int]bool, markComplete func(int)) error {
	type uploadPart struct {
		partNumber int
		uploadURL  string
	}
	completedParts := make(map[int]bool, len(completed))
	for partNumber, done := range completed {
		completedParts[partNumber] = done
	}
	var uploadParts []uploadPart
	for _, p := range createResp.Data.PartInfos {
		uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadURL})
	}
	// On resume the create response carries no part URLs, so the first batch
	// starts at part 1 (URLs fetched fresh via /file/getUploadUrl instead of
	// reusing stale presigned URLs).
	firstURLBatch := 101
	if len(createResp.Data.PartInfos) == 0 {
		firstURLBatch = 1
	}
	for i := firstURLBatch; i <= len(partInfos); i += 100 {
		end := i + 100
		if end > len(partInfos) {
			end = len(partInfos)
		}
		batchPartInfos := partInfos[i-1 : end]
		moreData := map[string]interface{}{
			"fileId":    createResp.Data.FileID,
			"uploadId":  createResp.Data.UploadID,
			"partInfos": batchPartInfos,
			"commonAccountInfo": map[string]interface{}{
				"account":     d.cl.getAccount(),
				"accountType": 1,
			},
		}
		var moreResp personalUploadURLResp
		if err := d.cl.personalPost(ctx, "/file/getUploadUrl", moreData, &moreResp); err != nil {
			return fmt.Errorf("139: upload get urls: %w", err)
		}
		if !moreResp.Success {
			return fmt.Errorf("139: upload get urls failed (code=%s): %s", moreResp.Code, moreResp.Message)
		}
		for _, p := range moreResp.Data.PartInfos {
			uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadURL})
		}
	}

	g, uploadCtx := errgroup.WithContext(ctx)
	g.SetLimit(uploadPartConcurrency)
	for _, up := range uploadParts {
		up := up
		g.Go(func() error {
			start := int64(up.partNumber-1) * partSize
			end := start + partSize
			if end > size {
				end = size
			}
			if completedParts[up.partNumber] {
				drive.ReportUploadProgress(progress, end-start)
				return nil
			}
			f, err := source.Open(uploadCtx)
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			defer f.Close()
			if _, err := f.Seek(start, io.SeekStart); err != nil {
				return fmt.Errorf("139: upload part %d seek: %w", up.partNumber, err)
			}
			body := drive.NewUploadProgressReader(progress, io.LimitReader(f, end-start))
			body = d.limiter.LimitUpload(uploadCtx, body)
			req, err := http.NewRequestWithContext(uploadCtx, http.MethodPut, up.uploadURL, body)
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			req.ContentLength = end - start
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set("Origin", defaultBaseURL)
			req.Header.Set("Referer", defaultBaseURL+"/")
			httpStart := time.Now()
			resp, err := d.cl.httpClient.Do(req)
			d.cl.recordMetric(uploadCtx, drive.MetricEvent{
				Operation: "upload_part",
				Method:    req.Method,
				URL:       driverutil.URL(req.URL),
				Status:    responseStatus(resp),
				Duration:  time.Since(httpStart).String(),
				Request:   map[string]any{"part_number": up.partNumber, "bytes": req.ContentLength},
				Error:     errorString(err),
			})
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err := fmt.Errorf("139: upload part %d: status %d", up.partNumber, resp.StatusCode)
				if nonRetryableUploadStatus(resp.StatusCode) {
					err = drive.NonRetryable(err)
				}
				return err
			}
			if markComplete != nil {
				markComplete(up.partNumber)
			}
			return nil
		})
	}
	return g.Wait()
}

func nonRetryableUploadStatus(status int) bool {
	return httputil.IsNonRetryableClientStatus(status)
}

func calcPartSize(fileSize int64) int64 {
	if fileSize/util.GiB > 30 {
		return 512 * util.MiB
	}
	return 100 * util.MiB
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, path)
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, p string) (string, error) {
	currentID := d.resolveID(rootID)
	p = strings.Trim(p, "/")
	if p == "" {
		return currentID, nil
	}
	for _, segment := range strings.Split(p, "/") {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir && entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: directory %q not found under %q", drive.ErrNotFound, segment, p)
		}
	}
	return currentID, nil
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.StateStoreInstaller = (*Driver)(nil)
var _ drive.BandwidthLimitInstaller = (*Driver)(nil)
