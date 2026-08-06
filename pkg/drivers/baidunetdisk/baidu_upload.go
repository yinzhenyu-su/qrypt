package baidunetdisk

import (
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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/retry"
	"github.com/yinzhenyu/qrypt/internal/util"
	"github.com/yinzhenyu/qrypt/internal/util/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	size := source.Size()
	if size < 1 {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("baidu_netdisk: empty files are not allowed by baidu netdisk"))
	}
	parentPath := d.resolvePath(parentID)
	remotePath := path.Join(parentPath, name)
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	blockList, contentMD5, sliceMD5, err := uploadHashes(ctx, source, size)
	if err != nil {
		return drive.Entry{}, err
	}
	blockListJSON, err := json.Marshal(blockList)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := uploadsession.UploadSessionKey(parentPath, name, size, contentMD5, sliceMD5)
	session, resumedSession := d.loadUploadSession(sessionKey)
	uploadID := session.UploadID
	partsToUpload := append([]int(nil), session.BlockList...)
	if !resumedSession {
		var pre precreateResp
		if err := d.precreate(ctx, remotePath, size, string(blockListJSON), contentMD5, sliceMD5, &pre); err != nil {
			err = fmt.Errorf("baidu_netdisk: upload precreate: %w", err)
			d.setLastError(err)
			return drive.Entry{}, err
		}
		if pre.ReturnType == 2 {
			drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
			d.lastErrorMu.Lock()
			d.instantUploadCount++
			d.lastErrorMu.Unlock()
			d.deleteUploadSession(sessionKey)
			return pre.File.entry(parentPath), nil
		}
		if pre.UploadID == "" {
			return drive.Entry{}, drive.NonRetryable(fmt.Errorf("baidu_netdisk: upload precreate returned empty uploadid"))
		}
		uploadID = pre.UploadID
		partsToUpload = append([]int(nil), pre.BlockList...)
		session = baiduUploadSession{
			Key:            sessionKey,
			ParentPath:     parentPath,
			Name:           name,
			RemotePath:     remotePath,
			Size:           size,
			ContentMD5:     contentMD5,
			SliceMD5:       sliceMD5,
			UploadID:       uploadID,
			PartSize:       uploadPartSize(size),
			BlockList:      partsToUpload,
			CompletedParts: map[int]bool{},
		}
	} else if session.CompletedParts == nil {
		session.CompletedParts = map[int]bool{}
	}
	if err := d.uploadParts(ctx, source, req.Progress, remotePath, name, size, uploadID, partsToUpload, session.CompletedParts, func(partSeq int) {
		session.CompletedParts[partSeq] = true
		d.saveUploadSession(session)
	}); err != nil {
		err = d.resumedUploadSessionError(resumedSession, sessionKey, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	var created createResp
	if err := d.createFile(ctx, remotePath, size, uploadID, string(blockListJSON), &created); err != nil {
		err = fmt.Errorf("baidu_netdisk: upload create: %w", err)
		err = d.resumedUploadSessionError(resumedSession, sessionKey, err)
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
	d.deleteUploadSession(sessionKey)
	return entry, nil
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

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, remotePath, name string, size int64, uploadID string, blockList []int, completed map[int]bool, markComplete func(int)) error {
	file, err := source.Open(ctx)
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
		if completed[partSeq] {
			drive.ReportUploadProgress(progress, length)
			continue
		}
		section := io.NewSectionReader(file, offset, length)
		if err := d.uploadSlice(ctx, progress, remotePath, name, uploadID, partSeq, section); err != nil {
			return err
		}
		if markComplete != nil {
			markComplete(partSeq)
		}
	}
	return nil
}

func (d *Driver) uploadSlice(ctx context.Context, progress drive.UploadProgress, remotePath, name, uploadID string, partSeq int, section *io.SectionReader) error {
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

	body, contentType, contentLength, err := multipartUploadBody(ctx, d.limiter, progress, name, section)
	if err != nil {
		return err
	}
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = contentLength
	start := time.Now()
	resp, err := d.httpClient.Do(req)
	d.recordHTTP(ctx, "upload_part", req, resp, start, map[string]any{"part_seq": partSeq, "bytes": req.ContentLength}, err)
	if err != nil {
		return fmt.Errorf("baidu_netdisk: upload part %d: %w", partSeq, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := util.HTTPError(fmt.Sprintf("baidu_netdisk: upload part %d", partSeq), nil, resp, data)
		if uploadIDExpiredResponse(data) {
			return errBaiduUploadIDExpired
		}
		if nonRetryableUploadStatus(resp.StatusCode) {
			err = drive.NonRetryable(err)
		}
		return err
	}
	if uploadIDExpiredResponse(data) {
		return errBaiduUploadIDExpired
	}
	var uploadResp uploadSliceResp
	if err := json.Unmarshal(data, &uploadResp); err == nil {
		if uploadResp.ErrorCode != 0 {
			return drive.NonRetryable(fmt.Errorf("baidu_netdisk: upload part %d error_code %d: %s", partSeq, uploadResp.ErrorCode, uploadResp.ErrorMsg))
		}
		if uploadResp.Errno != 0 {
			return drive.NonRetryable(fmt.Errorf("baidu_netdisk: upload part %d errno %d: %s", partSeq, uploadResp.Errno, uploadResp.Errmsg))
		}
	}
	return nil
}

func (d *Driver) request(ctx context.Context, method, rawURL string, params, form map[string]string, out any) error {
	var lastErr error
	for attempt := range 3 {
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
			if attempt < 2 {
				if waitErr := retry.Wait(ctx, attempt); waitErr != nil {
					return waitErr
				}
			}
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
	start := time.Now()
	resp, err := d.httpClient.Do(req)
	request := util.MergeRequest(util.RequestFields(params), util.RequestFields(form))
	d.recordHTTP(ctx, u.Path, req, resp, start, request, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return util.HTTPError("baidu_netdisk: upload session", nil, resp, data)
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

func (d *Driver) postForm(ctx context.Context, pathname string, params, form map[string]string, out any) error {
	return d.request(ctx, http.MethodPost, d.apiBaseURL+pathname, params, form, out)
}

func multipartUploadBody(ctx context.Context, limiter *drive.BandwidthLimiter, progress drive.UploadProgress, name string, section *io.SectionReader) (io.ReadCloser, string, int64, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentLength, err := multipartContentLength(mw.Boundary(), name, section.Size())
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, "", 0, err
	}
	go func() {
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			uploadReader := drive.NewUploadProgressReader(progress, section)
			reader := io.Reader(uploadReader)
			if limiter != nil {
				reader = limiter.LimitUpload(ctx, uploadReader)
			}
			_, err = io.Copy(part, reader)
		}
		if closeErr := mw.Close(); err == nil {
			err = closeErr
		}
		_ = pw.CloseWithError(err)
	}()
	return pr, mw.FormDataContentType(), contentLength, nil
}

func multipartContentLength(boundary, name string, payloadSize int64) (int64, error) {
	counter := countingWriter{}
	mw := multipart.NewWriter(&counter)
	if err := mw.SetBoundary(boundary); err != nil {
		return 0, err
	}
	if _, err := mw.CreateFormFile("file", name); err != nil {
		return 0, err
	}
	counter.n += payloadSize
	if err := mw.Close(); err != nil {
		return 0, err
	}
	return counter.n, nil
}

type countingWriter struct {
	n int64
}

func nonRetryableUploadStatus(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}

func uploadIDExpiredResponse(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "uploadid") &&
		(strings.Contains(lower, "invalid") || strings.Contains(lower, "expired") || strings.Contains(lower, "not found"))
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return apiErr.errno != 0
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "uploadid") &&
		(strings.Contains(s, "invalid") || strings.Contains(s, "expired") || strings.Contains(s, "not found"))
}

func entryFSID(entry drive.Entry) string {
	if extra, ok := drive.EntryRawExtra(entry).(map[string]any); ok {
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

func uploadHashes(ctx context.Context, source drive.ReadOnlyFileSource, size int64) ([]string, string, string, error) {
	file, err := source.Open(ctx)
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

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
