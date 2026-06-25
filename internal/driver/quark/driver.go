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

	"github.com/yinzhenyu/qrypt/internal/logging"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

type Driver struct {
	cl       *client
	urlCache sync.Map
	cookie   string
	rootPath string
	rootID   string
	limiter  *drive.RateLimiter
}

type uploadPartJob struct {
	number int
	data   []byte
}

type uploadPartResult struct {
	number int
	etag   string
	err    error
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

func (d *Driver) InstallRateLimiter(limiter *drive.RateLimiter) drive.RateLimitDirection {
	d.limiter = limiter
	return drive.RateLimitDownload | drive.RateLimitUpload
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
	start := time.Now()
	downloadURL, err := d.downloadURL(entry.ID)
	if err != nil {
		logging.L.Debugf("[quark] ReadURL fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(start))
		return nil, err
	}
	logging.L.Debugf("[quark] ReadURL fid=%q offset=%d size=%d dur=%s", entry.ID, offset, size, time.Since(start))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("quark: read: create request: %w", err)
	}
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}
	httpStart := time.Now()
	resp, err := d.cl.doDownload(req)
	if err != nil {
		d.invalidateURL(entry.ID)
		logging.L.Debugf("[quark] ReadHTTP fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(httpStart))
		return nil, fmt.Errorf("quark: read: download: %w", err)
	}
	logging.L.Debugf("[quark] ReadHTTP fid=%q offset=%d size=%d status=%d dur=%s", entry.ID, offset, size, resp.StatusCode, time.Since(httpStart))
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		d.invalidateURL(entry.ID)
		return d.Read(ctx, entry, offset, size)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
	}
	body := d.limiter.LimitDownload(ctx, resp.Body)
	return &traceReadCloser{
		ReadCloser: body,
		fid:        entry.ID,
		offset:     offset,
		size:       size,
		start:      time.Now(),
	}, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentID = d.resolve(parentID)
	logging.L.Infof("[QUARK] mkdir start parent=%q name=%q", parentID, name)
	data := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var resp createDirResp
	if err := d.cl.request(http.MethodPost, "/file", nil, data, &resp); err != nil {
		logging.L.Warnf("[QUARK] mkdir request failed parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, fmt.Errorf("quark: mkdir: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		logging.L.Warnf("[QUARK] mkdir api error parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, err
	}
	logging.L.Infof("[QUARK] mkdir complete parent=%q name=%q id=%q", parentID, name, resp.Data.Fid)
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
	putStart := time.Now()
	mtime := time.Now()
	logging.L.Infof("[QUARK] upload start parent=%q name=%q size=%d", parentID, name, size)
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
		logging.L.Warnf("[QUARK] upload pre failed parent=%q name=%q size=%d err=%v", parentID, name, size, err)
		return drive.Entry{}, fmt.Errorf("quark: upload pre: %w", err)
	}
	if err := apiError(preResp.respEnvelope); err != nil {
		logging.L.Warnf("[QUARK] upload pre api error parent=%q name=%q size=%d err=%v", parentID, name, size, err)
		return drive.Entry{}, err
	}
	logging.L.Infof("[QUARK] upload pre ok name=%q task=%q upload_id=%q part_size=%d finish=%t", name, preResp.Data.TaskID, preResp.Data.UploadID, preResp.Metadata.PartSize, preResp.Data.Finish)
	if preResp.Data.Finish && preResp.Data.Fid != "" {
		finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			logging.L.Warnf("[QUARK] instant upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		logging.L.Infof("[QUARK] instant upload complete name=%q fid=%q size=%d dur=%s", name, finalFid, size, time.Since(putStart))
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size}, nil
	}

	partSize := preResp.Metadata.PartSize
	if partSize <= 0 {
		partSize = 4 * 1024 * 1024
	}
	if partSize >= 4*1024*1024 && partSize < 16*1024*1024 {
		partSize = 16 * 1024 * 1024
	}

	md5Hash := md5.New()
	sha1Hash := sha1.New()
	teeReader := io.TeeReader(body, io.MultiWriter(md5Hash, sha1Hash))

	buf := make([]byte, partSize)
	etagsByPart := map[int]string{}
	var totalRead int64
	var submittedParts int
	var completedParts int
	partNumber := 1
	jobs := make(chan uploadPartJob, partUploadWorkers)
	results := make(chan uploadPartResult, partUploadWorkers)
	done := make(chan struct{})
	jobsClosed := false
	closeJobs := func() {
		if !jobsClosed {
			close(jobs)
			jobsClosed = true
		}
	}
	defer close(done)
	defer closeJobs()

	var uploadWG sync.WaitGroup
	for i := 0; i < partUploadWorkers; i++ {
		uploadWG.Add(1)
		go func() {
			defer uploadWG.Done()
			for {
				select {
				case job, ok := <-jobs:
					if !ok {
						return
					}
					logging.L.Debugf("[QUARK] upload part start name=%q task=%q part=%d bytes=%d", name, preResp.Data.TaskID, job.number, len(job.data))
					etag, err := d.uploadPart(ctx, &preResp, job.number, job.data)
					if err != nil {
						logging.L.Warnf("[QUARK] upload part failed name=%q task=%q part=%d bytes=%d err=%v", name, preResp.Data.TaskID, job.number, len(job.data), err)
					} else {
						logging.L.Debugf("[QUARK] upload part complete name=%q task=%q part=%d etag=%q", name, preResp.Data.TaskID, job.number, etag)
					}
					select {
					case results <- uploadPartResult{number: job.number, etag: etag, err: err}:
					case <-done:
						return
					}
				case <-done:
					return
				}
			}
		}()
	}
	handleResult := func(result uploadPartResult) error {
		if result.err != nil {
			return fmt.Errorf("quark: upload part %d: %w", result.number, result.err)
		}
		etagsByPart[result.number] = result.etag
		return nil
	}
	receiveResult := func() error {
		result := <-results
		completedParts++
		return handleResult(result)
	}
	sendJob := func(job uploadPartJob) error {
		for {
			select {
			case jobs <- job:
				submittedParts++
				return nil
			case result := <-results:
				completedParts++
				if err := handleResult(result); err != nil {
					return err
				}
			}
		}
	}

	for {
		n, readErr := io.ReadFull(teeReader, buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err := sendJob(uploadPartJob{number: partNumber, data: data}); err != nil {
				return drive.Entry{}, err
			}
			totalRead += int64(n)
			partNumber++
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			logging.L.Warnf("[QUARK] upload read body failed name=%q task=%q total_read=%d err=%v", name, preResp.Data.TaskID, totalRead, readErr)
			return drive.Entry{}, fmt.Errorf("quark: upload: read body: %w", readErr)
		}
	}
	closeJobs()
	for completedParts < submittedParts {
		if err := receiveResult(); err != nil {
			return drive.Entry{}, err
		}
	}
	uploadWG.Wait()

	etags := make([]string, 0, submittedParts)
	for i := 1; i <= submittedParts; i++ {
		etags = append(etags, etagsByPart[i])
	}
	if totalRead == 0 {
		logging.L.Debugf("[QUARK] upload empty part start name=%q task=%q", name, preResp.Data.TaskID)
		etag, err := d.uploadPart(ctx, &preResp, 1, []byte{})
		if err != nil {
			logging.L.Warnf("[QUARK] upload empty part failed name=%q task=%q err=%v", name, preResp.Data.TaskID, err)
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
		logging.L.Warnf("[QUARK] upload hash update failed name=%q task=%q total_read=%d err=%v", name, preResp.Data.TaskID, totalRead, err)
		return drive.Entry{}, fmt.Errorf("quark: upload hash: %w", err)
	}
	logging.L.Infof("[QUARK] upload hash update ok name=%q task=%q total_read=%d finish=%t", name, preResp.Data.TaskID, totalRead, hashResp.Data.Finish)
	if hashResp.Data.Finish {
		if hashResp.Data.Fid != "" {
			preResp.Data.Fid = hashResp.Data.Fid
		}
		finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			logging.L.Warnf("[QUARK] hash-finished upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		logging.L.Infof("[QUARK] hash-finished upload complete name=%q fid=%q size=%d", name, finalFid, totalRead)
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead}, nil
	}
	if err := d.ossComplete(&preResp, etags); err != nil {
		logging.L.Warnf("[QUARK] upload complete multipart failed name=%q task=%q parts=%d err=%v", name, preResp.Data.TaskID, len(etags), err)
		return drive.Entry{}, fmt.Errorf("quark: upload complete: %w", err)
	}
	finalFid, err := d.uploadFinish(preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
	if err != nil {
		logging.L.Warnf("[QUARK] upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
		return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
	}
	logging.L.Infof("[QUARK] upload complete name=%q fid=%q size=%d parts=%d dur=%s", name, finalFid, totalRead, len(etags), time.Since(putStart))
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
		ossPath := pre.Data.ObjKey
		if pre.Data.Bucket != "" {
			ossPath = pre.Data.Bucket + "/" + ossPath
		}
		authMeta := fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s?uploadId=%s",
			contentMd5, timeStr, callbackB64, timeStr, ossPath, pre.Data.UploadID)
		var authResp upAuthResp
		err := d.cl.request(http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info": pre.Data.AuthInfo,
			"auth_meta": authMeta,
			"task_id":   pre.Data.TaskID,
		}, &authResp)
		if err != nil {
			if attempt < ossMaxRetries {
				logging.L.Warnf("[QUARK] oss complete auth failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				time.Sleep(retryBackoff(attempt))
				continue
			}
			logging.L.Warnf("[QUARK] oss complete auth failed task=%q attempts=%d err=%v", pre.Data.TaskID, attempt+1, err)
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
				logging.L.Warnf("[QUARK] oss complete http failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				time.Sleep(retryBackoff(attempt))
				continue
			}
			logging.L.Warnf("[QUARK] oss complete http failed task=%q attempts=%d err=%v", pre.Data.TaskID, attempt+1, err)
			return fmt.Errorf("oss complete: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if attempt < ossMaxRetries {
			logging.L.Warnf("[QUARK] oss complete status retry task=%q attempt=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
			time.Sleep(retryBackoff(attempt))
			continue
		}
		logging.L.Warnf("[QUARK] oss complete status failed task=%q attempts=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
		return fmt.Errorf("oss complete status %d", resp.StatusCode)
	}
	return nil
}

func (d *Driver) uploadPart(ctx context.Context, pre *upPreResp, partNumber int, data []byte) (string, error) {
	logging.L.Infof("[QUARK] upload part enter task=%q part=%d bytes=%d bucket=%q obj=%q upload_url=%q", pre.Data.TaskID, partNumber, len(data), pre.Data.Bucket, pre.Data.ObjKey, pre.Data.UploadURL)
	for attempt := 0; attempt <= ossMaxRetries; attempt++ {
		dateStr := time.Now().UTC().Format(http.TimeFormat)
		ossPath := pre.Data.ObjKey
		if pre.Data.Bucket != "" {
			ossPath = pre.Data.Bucket + "/" + ossPath
		}
		authMeta := fmt.Sprintf("PUT\n\napplication/octet-stream\n%s\nx-oss-date:%s\nx-oss-user-agent:aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit\n/%s?partNumber=%d&uploadId=%s",
			dateStr, dateStr, ossPath, partNumber, pre.Data.UploadID)
		authStart := time.Now()
		var authResp upAuthResp
		err := d.cl.request(http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info":   pre.Data.AuthInfo,
			"auth_meta":   authMeta,
			"task_id":     pre.Data.TaskID,
			"part_number": partNumber,
		}, &authResp)
		authDur := time.Since(authStart)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < ossMaxRetries {
				logging.L.Warnf("[QUARK] upload part auth failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, retryBackoff(attempt)); err != nil {
					return "", err
				}
				continue
			}
			logging.L.Warnf("[QUARK] upload part auth failed task=%q part=%d attempts=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
			return "", err
		}

		logging.L.Infof("[QUARK] upload part auth done task=%q part=%d auth=%s", pre.Data.TaskID, partNumber, authDur)
		ossURLStr := ossURL(pre) + "?partNumber=" + strconv.Itoa(partNumber) + "&uploadId=" + pre.Data.UploadID
		logging.L.Infof("[QUARK] upload part oss put start task=%q part=%d url=%q", pre.Data.TaskID, partNumber, ossURLStr)
		ossStart := time.Now()
		body := d.limiter.LimitUpload(ctx, bytes.NewReader(data))
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, ossURLStr, body)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("x-oss-date", dateStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		resp, err := d.cl.httpClient.Do(req)
		ossDur := time.Since(ossStart)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < ossMaxRetries {
				logging.L.Warnf("[QUARK] upload part http failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, retryBackoff(attempt)); err != nil {
					return "", err
				}
				continue
			}
			logging.L.Warnf("[QUARK] upload part http failed task=%q part=%d attempts=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
			return "", fmt.Errorf("upload part %d http: %w", partNumber, err)
		}
		etag := resp.Header.Get("Etag")
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logging.L.Infof("[QUARK] upload part done task=%q part=%d bytes=%d auth=%s oss=%s", pre.Data.TaskID, partNumber, len(data), authDur, ossDur)
			return etag, nil
		}
		if attempt < ossMaxRetries {
			logging.L.Warnf("[QUARK] upload part status retry task=%q part=%d attempt=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
			if err := sleepContext(ctx, retryBackoff(attempt)); err != nil {
				return "", err
			}
			continue
		}
		logging.L.Warnf("[QUARK] upload part status failed task=%q part=%d attempts=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
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
	if pre.Data.Bucket != "" {
		return fmt.Sprintf("https://%s.%s/%s", pre.Data.Bucket, host, pre.Data.ObjKey)
	}
	return fmt.Sprintf("https://%s/%s", host, pre.Data.ObjKey)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

type traceReadCloser struct {
	io.ReadCloser
	fid    string
	offset int64
	size   int64
	start  time.Time
	read   int64
}

func (r *traceReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	return n, err
}

func (r *traceReadCloser) Close() error {
	err := r.ReadCloser.Close()
	logging.L.Debugf("[quark] ReadBody fid=%q offset=%d size=%d read=%d err=%v dur=%s", r.fid, r.offset, r.size, r.read, err, time.Since(r.start))
	return err
}

var _ drive.Driver = (*Driver)(nil)
var _ drive.Writer = (*Driver)(nil)
var _ drive.Uploader = (*Driver)(nil)
var _ drive.PathResolver = (*Driver)(nil)
var _ drive.RateLimitInstaller = (*Driver)(nil)
