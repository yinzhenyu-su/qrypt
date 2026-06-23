package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Driver struct {
	cl       *client
	urlCache sync.Map
	cookie   string
	rootPath string
	rootID   string
}

func init() {
	drive.Register("quark", func(params drive.Params) (drive.Driver, error) {
		cookie := params["cookie"]
		if cookie == "" {
			return nil, fmt.Errorf("quark: missing cookie")
		}
		return New(cookie, Options{
			RootPath: params["root_path"],
			RootID:   params["root_id"],
			BaseURL:  params["base_url"],
			V2URL:    params["v2_url"],
		}), nil
	})
}

type Options struct {
	RootPath string
	RootID   string
	BaseURL  string
	V2URL    string
}

func New(cookie string, opts Options) *Driver {
	rootID := opts.RootID
	if rootID == "" {
		rootID = "0"
	}
	return &Driver{
		cl:       newClient(cookie, clientOptions{BaseURL: opts.BaseURL, V2URL: opts.V2URL}),
		cookie:   cookie,
		rootPath: opts.RootPath,
		rootID:   rootID,
	}
}

func (d *Driver) Init(ctx context.Context) error {
	if d.cookie == "" {
		return fmt.Errorf("quark: cookie is required")
	}
	var resp sortResp
	if err := d.cl.request(http.MethodGet, "/file/sort", map[string]string{
		"pdir_fid": d.rootID,
		"_size":    "1",
	}, nil, &resp); err != nil {
		return fmt.Errorf("quark: validate cookie: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return err
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

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolve(parentID)
	pageSize := 100
	var firstResp sortResp
	err := d.cl.request(http.MethodGet, "/file/sort", map[string]string{
		"pdir_fid":             parentID,
		"_size":                strconv.Itoa(pageSize),
		"_page":                "1",
		"_fetch_total":         "1",
		"fetch_all_file":       "1",
		"fetch_risk_file_name": "1",
	}, nil, &firstResp)
	if err != nil {
		return nil, fmt.Errorf("quark: list: %w", err)
	}
	if err := apiError(firstResp.respEnvelope); err != nil {
		return nil, err
	}

	total := firstResp.Metadata.Total
	if total < len(firstResp.Data.List) {
		total = len(firstResp.Data.List)
	}
	allFiles := make([]file, total)
	copy(allFiles, firstResp.Data.List)

	if total > pageSize {
		totalPages := (total + pageSize - 1) / pageSize
		var wg sync.WaitGroup
		var once sync.Once
		var lastErr error
		for page := 2; page <= totalPages; page++ {
			wg.Add(1)
			go func(page int) {
				defer wg.Done()
				var resp sortResp
				err := d.cl.request(http.MethodGet, "/file/sort", map[string]string{
					"pdir_fid":             parentID,
					"_size":                strconv.Itoa(pageSize),
					"_page":                strconv.Itoa(page),
					"fetch_all_file":       "1",
					"fetch_risk_file_name": "1",
				}, nil, &resp)
				if err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				if err := apiError(resp.respEnvelope); err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				offset := (page - 1) * pageSize
				if offset < len(allFiles) {
					copy(allFiles[offset:], resp.Data.List)
				}
			}(page)
		}
		wg.Wait()
		if lastErr != nil {
			return nil, lastErr
		}
	}

	entries := make([]drive.Entry, 0, len(allFiles))
	for _, item := range allFiles {
		if item.Fid == "" {
			continue
		}
		entries = append(entries, item.entry(parentID))
	}
	return entries, nil
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	downloadURL, err := d.downloadURL(entry.ID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("quark: read: create request: %w", err)
	}
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}
	resp, err := d.cl.doDownload(req)
	if err != nil {
		d.invalidateURL(entry.ID)
		return nil, fmt.Errorf("quark: read: download: %w", err)
	}
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		d.invalidateURL(entry.ID)
		return d.Read(ctx, entry, offset, size)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentID = d.resolve(parentID)
	data := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var resp createDirResp
	if err := d.cl.request(http.MethodPost, "/file", nil, data, &resp); err != nil {
		return drive.Entry{}, fmt.Errorf("quark: mkdir: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return drive.Entry{}, err
	}
	return drive.Entry{ID: resp.Data.Fid, ParentID: parentID, Name: name, IsDir: true}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	data := map[string]any{
		"filelist":     []string{entry.ID},
		"to_pdir_fid":  d.resolve(dstParentID),
		"action_type":  1,
		"exclude_fids": []string{},
	}
	var resp respEnvelope
	if err := d.cl.request(http.MethodPost, "/file/move", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: move: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	data := map[string]any{
		"fid":       entry.ID,
		"file_name": newName,
	}
	var resp respEnvelope
	if err := d.cl.request(http.MethodPost, "/file/rename", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: rename: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	data := map[string]any{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{entry.ID},
	}
	var resp respEnvelope
	if err := d.cl.request(http.MethodPost, "/file/delete", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: delete: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Put(ctx context.Context, parentID, name string, size int64, body io.Reader) (drive.Entry, error) {
	parentID = d.resolve(parentID)
	mtime := time.Now()
	preData := map[string]any{
		"ccp_hash_update": true,
		"file_name":       name,
		"l_created_at":    mtime.UnixMilli(),
		"l_updated_at":    mtime.UnixMilli(),
		"pdir_fid":        parentID,
		"size":            size,
		"format_type":     0,
	}
	var preResp upPreResp
	if err := d.cl.request(http.MethodPost, "/file/upload/pre", nil, preData, &preResp); err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload pre: %w", err)
	}
	if err := apiError(preResp.respEnvelope); err != nil {
		return drive.Entry{}, err
	}
	if preResp.Data.Finish && preResp.Data.Fid != "" {
		finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size}, nil
	}

	partSize := preResp.Metadata.PartSize
	if partSize <= 0 {
		partSize = 4 * 1024 * 1024
	}

	md5Hash := md5.New()
	sha1Hash := sha1.New()
	teeReader := io.TeeReader(body, io.MultiWriter(md5Hash, sha1Hash))

	buf := make([]byte, partSize)
	var etags []string
	var totalRead int64
	partNumber := 1
	for {
		n, readErr := io.ReadFull(teeReader, buf)
		if n > 0 {
			etag, err := d.uploadPart(&preResp, partNumber, buf[:n])
			if err != nil {
				return drive.Entry{}, fmt.Errorf("quark: upload part %d: %w", partNumber, err)
			}
			etags = append(etags, etag)
			totalRead += int64(n)
			partNumber++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return drive.Entry{}, fmt.Errorf("quark: upload: read body: %w", readErr)
		}
	}
	if totalRead == 0 {
		etag, err := d.uploadPart(&preResp, 1, []byte{})
		if err != nil {
			return drive.Entry{}, fmt.Errorf("quark: upload part 1: %w", err)
		}
		etags = append(etags, etag)
	}

	hashData := map[string]any{
		"md5":     fmt.Sprintf("%X", md5Hash.Sum(nil)),
		"sha1":    fmt.Sprintf("%X", sha1Hash.Sum(nil)),
		"task_id": preResp.Data.TaskID,
	}
	var hashResp hashResp
	if err := d.cl.request(http.MethodPost, "/file/update/hash", nil, hashData, &hashResp); err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload hash: %w", err)
	}
	if hashResp.Data.Finish {
		if hashResp.Data.Fid != "" {
			preResp.Data.Fid = hashResp.Data.Fid
		}
		finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead}, nil
	}
	if err := d.ossComplete(&preResp, etags); err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload complete: %w", err)
	}
	finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
	}
	return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead}, nil
}

func (d *Driver) ResolvePath(ctx context.Context, path string) (string, error) {
	return d.resolvePathFrom(ctx, d.rootID, path)
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, path string) (string, error) {
	currentID := d.resolve(rootID)
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" {
			continue
		}
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("quark: child not found: %s", segment)
		}
	}
	return currentID, nil
}

func (d *Driver) resolve(id string) string {
	if id == "" || id == "0" || id == "/" {
		return d.rootID
	}
	return id
}

func (d *Driver) getURL(fid string) (string, bool) {
	if value, ok := d.urlCache.Load(fid); ok {
		cached := value.(cachedURL)
		if time.Now().Before(cached.expiry) {
			return cached.url, true
		}
	}
	return "", false
}

func (d *Driver) setURL(fid, url string) {
	d.urlCache.Store(fid, cachedURL{url: url, expiry: time.Now().Add(10 * time.Minute)})
}

func (d *Driver) invalidateURL(fid string) {
	d.urlCache.Delete(fid)
}

func (d *Driver) downloadURL(fid string) (string, error) {
	if url, ok := d.getURL(fid); ok {
		return url, nil
	}
	var resp downResp
	if err := d.cl.request(http.MethodPost, "/file/download", nil, map[string]any{
		"fids": []string{fid},
	}, &resp); err != nil {
		return "", fmt.Errorf("quark: get download url: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return "", err
	}
	if len(resp.Data) == 0 {
		return "", fmt.Errorf("quark: no download url found")
	}
	url := resp.Data[0].DownloadURL
	d.setURL(fid, url)
	return url, nil
}

func (d *Driver) uploadFinish(fid, objKey, taskID string) (string, error) {
	var resp struct {
		respEnvelope
		Data struct {
			Fid string `json:"fid"`
		} `json:"data"`
	}
	if err := d.cl.request(http.MethodPost, "/file/upload/finish", nil, map[string]any{
		"obj_key": objKey,
		"task_id": taskID,
	}, &resp); err != nil {
		return fid, err
	}
	if err := apiError(resp.respEnvelope); err != nil {
		return fid, err
	}
	if resp.Data.Fid != "" {
		return resp.Data.Fid, nil
	}
	return fid, nil
}

func (d *Driver) ossComplete(pre *upPreResp, etags []string) error {
	if len(etags) == 0 {
		return nil
	}
	var xmlBody strings.Builder
	xmlBody.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUpload>`)
	for i, etag := range etags {
		xmlBody.WriteString(fmt.Sprintf(`
<Part>
<PartNumber>%d</PartNumber>
<ETag>%s</ETag>
</Part>`, i+1, etag))
	}
	xmlBody.WriteString(`
</CompleteMultipartUpload>`)
	body := xmlBody.String()
	bodyMD5 := md5.Sum([]byte(body))
	contentMd5 := base64.StdEncoding.EncodeToString(bodyMD5[:])

	for attempt := 0; attempt <= ossMaxRetries; attempt++ {
		timeStr := time.Now().UTC().Format(http.TimeFormat)
		callbackB64 := base64.StdEncoding.EncodeToString(pre.Data.Callback)
		authMeta := fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?uploadId=%s",
			contentMd5, timeStr, callbackB64, timeStr, pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadID)
		var authResp upAuthResp
		err := d.cl.request(http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info": pre.Data.AuthInfo,
			"auth_meta": authMeta,
			"task_id":   pre.Data.TaskID,
		}, &authResp)
		if err != nil {
			if attempt < ossMaxRetries {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return err
		}

		req, err := http.NewRequest(http.MethodPost, ossURL(pre)+"?uploadId="+pre.Data.UploadID, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-MD5", contentMd5)
		req.Header.Set("Content-Type", "application/xml")
		req.Header.Set("x-oss-callback", callbackB64)
		req.Header.Set("x-oss-date", timeStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		req.Header.Set("User-Agent", defaultUserAgent)
		resp, err := d.cl.httpClient.Do(req)
		if err != nil {
			if attempt < ossMaxRetries {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return fmt.Errorf("oss complete: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if attempt < ossMaxRetries {
			time.Sleep(retryBackoff(attempt))
			continue
		}
		return fmt.Errorf("oss complete status %d", resp.StatusCode)
	}
	return nil
}

func (d *Driver) uploadPart(pre *upPreResp, partNumber int, data []byte) (string, error) {
	for attempt := 0; attempt <= ossMaxRetries; attempt++ {
		dateStr := time.Now().UTC().Format(http.TimeFormat)
		authMeta := fmt.Sprintf("PUT\n\napplication/octet-stream\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s/%s?partNumber=%d&uploadId=%s",
			dateStr, dateStr, pre.Data.Bucket, pre.Data.ObjKey, partNumber, pre.Data.UploadID)
		var authResp upAuthResp
		err := d.cl.request(http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info":   pre.Data.AuthInfo,
			"auth_meta":   authMeta,
			"task_id":     pre.Data.TaskID,
			"part_number": partNumber,
		}, &authResp)
		if err != nil {
			if attempt < ossMaxRetries {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return "", err
		}

		req, err := http.NewRequest(http.MethodPut, ossURL(pre)+"?partNumber="+strconv.Itoa(partNumber)+"&uploadId="+pre.Data.UploadID, bytes.NewReader(data))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("x-oss-date", dateStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		resp, err := d.cl.httpClient.Do(req)
		if err != nil {
			if attempt < ossMaxRetries {
				time.Sleep(retryBackoff(attempt))
				continue
			}
			return "", fmt.Errorf("upload part %d http: %w", partNumber, err)
		}
		etag := resp.Header.Get("Etag")
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return etag, nil
		}
		if attempt < ossMaxRetries {
			time.Sleep(retryBackoff(attempt))
			continue
		}
		return "", fmt.Errorf("upload part %d status %d", partNumber, resp.StatusCode)
	}
	return "", nil
}

func ossURL(pre *upPreResp) string {
	host := pre.Data.UploadURL
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if idx := strings.Index(host, "/"); idx != -1 {
		host = host[:idx]
	}
	return fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, host, pre.Data.ObjKey)
}

func apiError(resp respEnvelope) error {
	if resp.Status < 400 && resp.Code == 0 {
		return nil
	}
	switch resp.Code {
	case 23001:
		return fmt.Errorf("quark: not found")
	case 23004:
		return nil
	case 23008:
		return fmt.Errorf("quark: directory already exists")
	}
	return fmt.Errorf("quark: api error: status=%d code=%d msg=%s", resp.Status, resp.Code, resp.Message)
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.Uploader = (*Driver)(nil)
var _ drive.PathResolver = (*Driver)(nil)
