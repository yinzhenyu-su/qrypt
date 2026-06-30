package yun139

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"golang.org/x/sync/errgroup"
)

const maxUploadSize = 2 << 30

type partMeta struct {
	PartNumber int64 `json:"partNumber"`
	PartSize   int64 `json:"partSize"`
}

const uploadPartConcurrency = 4

type Driver struct {
	cl          *client
	rootID      string
	stateStore  drive.StateStore
	authSource  string
	authUpdated time.Time
	debugMu     sync.Mutex
}

type authState struct {
	Authorization string    `json:"authorization,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

func init() {
	drive.Register("139yun", func(params drive.Params) (drive.Driver, error) {
		auth := params["authorization"]
		if auth == "" {
			return nil, fmt.Errorf("139: missing authorization")
		}
		return New(auth, params["root_id"]), nil
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
			Name:        "root_id",
			Type:        "string",
			Description: "Root directory ID",
			Default:     "",
			Example:     "0",
		},
	)
}

func New(authorization, rootID string) *Driver {
	d := &Driver{
		cl:         newClient(authorization),
		rootID:     rootID,
		authSource: "config",
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
	if d.rootID == "" {
		d.rootID = "/"
	}
	if err := d.cl.ensurePersonalCloudHost(); err != nil {
		return fmt.Errorf("139: resolve host: %w", err)
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
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
	if state.Authorization != "" {
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
		Authorization: authorization,
		UpdatedAt:     d.authUpdated,
	})
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

	resp, err := d.cl.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("139: read download: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("139: read status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (d *Driver) getDownloadURL(ctx context.Context, fileID string) (string, error) {
	data := map[string]interface{}{"fileId": fileID}
	var resp downloadResp
	err := d.cl.personalPost(ctx, "/file/getDownloadUrl", data, &resp)
	if err != nil {
		return "", fmt.Errorf("139: download url: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("139: download url failed (code=%s): %s", resp.Code, resp.Message)
	}
	if resp.Data.CdnUrl != "" {
		return resp.Data.CdnUrl, nil
	}
	return resp.Data.Url, nil
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
	return drive.Entry{ID: resp.Data.FileId, ParentID: fileID, Name: resp.Data.Name, IsDir: true, ModTime: now}, nil
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

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	if size > maxUploadSize {
		return drive.Entry{}, fmt.Errorf("139: upload %s (%d bytes) exceeds max size (%d)", name, size, maxUploadSize)
	}
	tmpFile, err := os.CreateTemp("", "139-upload-*")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmpFile, body)
	if err != nil {
		tmpFile.Close()
		return drive.Entry{}, fmt.Errorf("139: upload write: %w", err)
	}
	tmpFile.Close()
	if written != size {
		return drive.Entry{}, fmt.Errorf("139: upload size mismatch: wrote %d, expected %d", written, size)
	}

	return d.PutFile(ctx, parentID, name, size, tmpPath)
}

func (d *Driver) PutFile(ctx context.Context, parentID, name string, size int64, localPath string) (drive.Entry, error) {
	if size > maxUploadSize {
		return drive.Entry{}, fmt.Errorf("139: upload %s (%d bytes) exceeds max size (%d)", name, size, maxUploadSize)
	}
	stat, err := os.Stat(localPath)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload stat: %w", err)
	}
	if stat.Size() != size {
		return drive.Entry{}, fmt.Errorf("139: upload size mismatch: file has %d, expected %d", stat.Size(), size)
	}
	return d.putFile(ctx, parentID, name, size, localPath)
}

func (d *Driver) HealthCheck(ctx context.Context) drive.HealthStatus {
	start := time.Now()
	status := drive.HealthStatus{Driver: "139yun", CheckedAt: start}
	_, err := d.List(ctx, d.rootID)
	status.Latency = time.Since(start).String()
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.OK = true
	return status
}

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      "139yun",
		Health:      "unknown",
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"root_id":     d.rootID,
			"auth_source": d.authSource,
		},
		Extra: map[string]any{
			"auth_updated_at": d.authUpdated,
		},
	}, nil
}

func (d *Driver) putFile(ctx context.Context, parentID, name string, size int64, localPath string) (drive.Entry, error) {
	now := time.Now()
	fileID := d.resolveID(parentID)
	partSize := calcPartSize(size)

	hashFile, err := os.Open(localPath)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload hash open: %w", err)
	}
	hasher := sha256.New()
	hashed, err := io.Copy(hasher, hashFile)
	closeErr := hashFile.Close()
	if err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload hash: %w", err)
	}
	if closeErr != nil {
		return drive.Entry{}, fmt.Errorf("139: upload hash close: %w", closeErr)
	}
	if hashed != size {
		return drive.Entry{}, fmt.Errorf("139: upload size mismatch: hashed %d, expected %d", hashed, size)
	}
	sha256Hex := fmt.Sprintf("%X", hasher.Sum(nil))
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
		"parallelUpload":       true,
		"partInfos":            partInfos,
		"size":                 size,
		"parentFileId":         fileID,
		"name":                 name,
		"type":                 "file",
		"fileRenameMode":       "auto_rename",
	}
	var createResp personalUploadResp
	if err := d.cl.personalPost(ctx, "/file/create", createData, &createResp); err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload create: %w", err)
	}
	if !createResp.Success {
		return drive.Entry{}, fmt.Errorf("139: upload create failed (code=%s): %s", createResp.Code, createResp.Message)
	}

	logging.L.Debugf("[139] upload create: fileId=%s exist=%v rapid=%v parts=%d uploadId=%s",
		createResp.Data.FileId, createResp.Data.Exist, createResp.Data.RapidUpload,
		len(createResp.Data.PartInfos), createResp.Data.UploadId)

	if createResp.Data.Exist {
		return drive.Entry{ID: createResp.Data.FileId, ParentID: fileID, Name: name, Size: size, ModTime: now}, nil
	}

	// Server returns upload URLs when it needs multipart upload.
	if len(createResp.Data.PartInfos) > 0 {
		if err := d.uploadParts(ctx, createResp, partInfos, partSize, size, localPath); err != nil {
			return drive.Entry{}, err
		}
	}

	completeData := map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"fileId":               createResp.Data.FileId,
		"uploadId":             createResp.Data.UploadId,
	}
	logging.L.Debugf("[139] upload complete: fileId=%s uploadId=%s", createResp.Data.FileId, createResp.Data.UploadId)
	var completeResp baseResp
	if err := d.cl.personalPost(ctx, "/file/complete", completeData, &completeResp); err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload complete: %w", err)
	}
	if !completeResp.Success {
		return drive.Entry{}, fmt.Errorf("139: upload complete failed (code=%s): %s", completeResp.Code, completeResp.Message)
	}

	// Handle auto_rename conflict: server renamed our uploaded file because
	// a file with the same name already existed in the target directory.
	// The old file gets removed and our new file is renamed back to original.
	if createResp.Data.FileName != "" && createResp.Data.FileName != name {
		logging.L.Infof("[139] upload was renamed by server: %q -> %q (new id=%s)", name, createResp.Data.FileName, createResp.Data.FileId)

		// 1. Remove all stale duplicates with a different file ID
		entries, err := d.List(ctx, parentID)
		if err != nil {
			logging.L.Warnf("[139] failed to list files for conflict resolution: %v", err)
		} else {
			for _, e := range entries {
				if e.Name == name && !e.IsDir && e.ID != createResp.Data.FileId {
					logging.L.Infof("[139] removing duplicate file: name=%q id=%q (keeping new id=%q)", name, e.ID, createResp.Data.FileId)
					if err := d.Remove(ctx, e); err != nil {
						logging.L.Warnf("[139] failed to remove duplicate file id=%s: %v", e.ID, err)
					}
				}
			}
		}

		// 2. Rename our new file back to the original name using its stable
		// file ID (toEntry strips the suffix so the list name is ambiguous).
		if err := d.Rename(ctx, drive.Entry{ID: createResp.Data.FileId}, name); err != nil {
			logging.L.Warnf("[139] failed to rename new file id=%s back to %q: %v", createResp.Data.FileId, name, err)
			return drive.Entry{ID: createResp.Data.FileId, ParentID: fileID, Name: name, Size: size, ModTime: now}, nil
		}
	}

	return drive.Entry{ID: createResp.Data.FileId, ParentID: fileID, Name: name, Size: size, ModTime: now}, nil
}

func (d *Driver) uploadParts(ctx context.Context, createResp personalUploadResp, partInfos []partMeta, partSize, size int64, localPath string) error {
	type uploadPart struct {
		partNumber int
		uploadURL  string
	}
	var uploadParts []uploadPart
	for _, p := range createResp.Data.PartInfos {
		uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadUrl})
	}
	for i := 101; i <= len(partInfos); i += 100 {
		end := i + 100
		if end > len(partInfos) {
			end = len(partInfos)
		}
		batchPartInfos := partInfos[i-1 : end]
		moreData := map[string]interface{}{
			"fileId":    createResp.Data.FileId,
			"uploadId":  createResp.Data.UploadId,
			"partInfos": batchPartInfos,
			"commonAccountInfo": map[string]interface{}{
				"account":     d.cl.getAccount(),
				"accountType": 1,
			},
		}
		var moreResp personalUploadUrlResp
		if err := d.cl.personalPost(ctx, "/file/getUploadUrl", moreData, &moreResp); err != nil {
			return fmt.Errorf("139: upload get urls: %w", err)
		}
		if !moreResp.Success {
			return fmt.Errorf("139: upload get urls failed (code=%s): %s", moreResp.Code, moreResp.Message)
		}
		for _, p := range moreResp.Data.PartInfos {
			uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadUrl})
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
			f, err := os.Open(localPath)
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			defer f.Close()
			if _, err := f.Seek(start, io.SeekStart); err != nil {
				return fmt.Errorf("139: upload part %d seek: %w", up.partNumber, err)
			}
			req, err := http.NewRequestWithContext(uploadCtx, http.MethodPut, up.uploadURL, io.LimitReader(f, end-start))
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			req.ContentLength = end - start
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set("Origin", defaultBaseURL)
			req.Header.Set("Referer", defaultBaseURL+"/")
			resp, err := d.cl.httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("139: upload part %d: %w", up.partNumber, err)
			}
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("139: upload part %d: status %d", up.partNumber, resp.StatusCode)
			}
			return nil
		})
	}
	return g.Wait()
}

func calcPartSize(fileSize int64) int64 {
	switch {
	case fileSize <= 0:
		return 4 * 1024 * 1024
	case fileSize <= 100*1024*1024:
		return 4 * 1024 * 1024
	case fileSize <= 500*1024*1024:
		return 10 * 1024 * 1024
	case fileSize <= 1*1024*1024*1024:
		return 20 * 1024 * 1024
	default:
		return 50 * 1024 * 1024
	}
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	if path == "" || path == "/" {
		return d.rootID, nil
	}
	return d.rootID, nil
}
