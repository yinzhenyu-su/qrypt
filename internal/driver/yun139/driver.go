package yun139

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"golang.org/x/sync/errgroup"
)

const maxUploadSize = 2 << 30

const uploadPartConcurrency = 4

type Driver struct {
	cl      *client
	rootID  string
	debugMu sync.Mutex
}

func init() {
	drive.Register("139yun", func(params drive.Params) (drive.Driver, error) {
		auth := params["authorization"]
		if auth == "" {
			return nil, fmt.Errorf("139: missing authorization")
		}
		return New(auth, params["root_id"]), nil
	})
}

func New(authorization, rootID string) *Driver {
	return &Driver{
		cl:     newClient(authorization),
		rootID: rootID,
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if _, _, err := d.cl.decodeAuth(); err != nil {
		return fmt.Errorf("139: invalid authorization: %w", err)
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

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
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
	return drive.Entry{ID: resp.Data.FileId, Name: resp.Data.Name, IsDir: true}, nil
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

func (d *Driver) putFile(ctx context.Context, parentID, name string, size int64, localPath string) (drive.Entry, error) {
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

	// Build part metadata for the create call.
	type partMeta struct {
		PartNumber      int64 `json:"partNumber"`
		PartSize        int64 `json:"partSize"`
		ParallelHashCtx struct {
			PartOffset int64 `json:"partOffset"`
		} `json:"parallelHashCtx"`
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
			ParallelHashCtx: struct {
				PartOffset int64 `json:"partOffset"`
			}{PartOffset: start},
		}
	}

	firstPartInfos := partInfos
	if len(firstPartInfos) > 100 {
		firstPartInfos = firstPartInfos[:100]
	}

	createData := map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"contentType":          "application/octet-stream",
		"parallelUpload":       false,
		"partInfos":            firstPartInfos,
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

	logging.L.Debugf("139 upload create: fileId=%s exist=%v rapid=%v parts=%d uploadId=%s",
		createResp.Data.FileId, createResp.Data.Exist, createResp.Data.RapidUpload,
		len(createResp.Data.PartInfos), createResp.Data.UploadId)

	if createResp.Data.Exist {
		return drive.Entry{ID: createResp.Data.FileId, Name: name, Size: size}, nil
	}

	// Collect upload URLs.
	type uploadPart struct {
		partNumber int
		uploadURL  string
	}
	var uploadParts []uploadPart
	for _, p := range createResp.Data.PartInfos {
		uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadUrl})
	}

	// Fetch URLs for parts beyond the first 100.
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
			return drive.Entry{}, fmt.Errorf("139: upload get urls: %w", err)
		}
		if !moreResp.Success {
			return drive.Entry{}, fmt.Errorf("139: upload get urls failed (code=%s): %s", moreResp.Code, moreResp.Message)
		}
		for _, p := range moreResp.Data.PartInfos {
			uploadParts = append(uploadParts, uploadPart{partNumber: p.PartNumber, uploadURL: p.UploadUrl})
		}
	}

	// Upload parts concurrently.
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
			req.Header.Set("Content-Length", fmt.Sprint(end-start))
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
	if err := g.Wait(); err != nil {
		return drive.Entry{}, err
	}

	// Commit.
	completeData := map[string]interface{}{
		"contentHash":          sha256Hex,
		"contentHashAlgorithm": "SHA256",
		"fileId":               createResp.Data.FileId,
		"uploadId":             createResp.Data.UploadId,
	}
	var completeResp baseResp
	if err := d.cl.personalPost(ctx, "/file/complete", completeData, &completeResp); err != nil {
		return drive.Entry{}, fmt.Errorf("139: upload complete: %w", err)
	}
	if !completeResp.Success {
		return drive.Entry{}, fmt.Errorf("139: upload complete failed (code=%s): %s", completeResp.Code, completeResp.Message)
	}

	return drive.Entry{ID: createResp.Data.FileId, Name: name, Size: size}, nil
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
