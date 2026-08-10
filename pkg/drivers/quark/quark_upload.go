package quark

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"
	"github.com/yinzhenyu/qrypt/internal/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	size := source.Size()
	parentID = d.resolve(parentID)
	putStart := time.Now()
	mtime := req.ModTime
	if mtime.IsZero() {
		mtime = time.Now()
	}
	logging.L.InfofEvery("quark.upload_start", time.Second, "[QUARK] upload start parent=%q name=%q size=%d", parentID, name, size)

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	hashData, hasSourceHashes, err := quarkSourceHashes(source)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := ""
	var session quarkUploadSession
	var resumedSession bool
	if hasSourceHashes {
		sessionKey = d.uploadSessionKey(parentID, name, size, hashData)
		session, resumedSession = d.loadUploadSession(sessionKey)
	}

	var preResp upPreResp
	if resumedSession {
		preResp = session.preResp()
		logging.L.InfofEvery("quark.upload_resume", time.Second, "[QUARK] upload resume name=%q task=%q upload_id=%q completed_parts=%d", name, session.TaskID, session.UploadID, len(session.Etags))
	} else {
		preData := map[string]any{
			"ccp_hash_update": true,
			"file_name":       name,
			"l_created_at":    mtime.UnixMilli(),
			"l_updated_at":    mtime.UnixMilli(),
			"pdir_fid":        parentID,
			"size":            size,
			"format_type":     0,
		}
		if err := d.cl.request(ctx, http.MethodPost, "/file/upload/pre", nil, preData, &preResp); err != nil {
			logging.L.Warnf("[QUARK] upload pre failed parent=%q name=%q size=%d err=%v", parentID, name, size, err)
			return drive.Entry{}, fmt.Errorf("quark: upload pre: %w", err)
		}
		if err := apiError(preResp.respEnvelope); err != nil {
			logging.L.Warnf("[QUARK] upload pre api error parent=%q name=%q size=%d err=%v", parentID, name, size, err)
			return drive.Entry{}, err
		}
	}
	logging.L.InfofEvery("quark.upload_pre_ok", time.Second, "[QUARK] upload pre ok name=%q task=%q upload_id=%q part_size=%d finish=%t", name, preResp.Data.TaskID, preResp.Data.UploadID, preResp.Metadata.PartSize, preResp.Data.Finish)
	d.setUploadDebug(preResp.Data.TaskID, quarkUploadDebug{
		Name:       name,
		ParentID:   parentID,
		TaskID:     preResp.Data.TaskID,
		UploadID:   preResp.Data.UploadID,
		ObjKey:     preResp.Data.ObjKey,
		PartSize:   int64(preResp.Metadata.PartSize),
		BytesTotal: size,
		Stage:      "pre_ok",
		StartedAt:  putStart,
		UpdatedAt:  time.Now(),
	})
	defer d.finishUploadDebug(preResp.Data.TaskID)
	if preResp.Data.Finish && preResp.Data.Fid != "" {
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
		d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) {
			item.Stage = "instant_finish"
			item.BytesRead = size
		})
		finalFid, err := d.uploadFinish(ctx, preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
		if err != nil {
			d.setUploadDebugError(preResp.Data.TaskID, err)
			logging.L.Warnf("[QUARK] instant upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
			return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
		}
		logging.L.InfofEvery("quark.instant_upload_complete", time.Second, "[QUARK] instant upload complete name=%q fid=%q size=%d dur=%s", name, finalFid, size, time.Since(putStart))
		d.debugMu.Lock()
		d.instantUploadCount++
		d.debugMu.Unlock()
		d.deleteUploadSession(sessionKey)
		return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size, ModTime: mtime, CreatedAt: mtime, UpdatedAt: mtime}, nil
	}

	partSize := preResp.Metadata.PartSize
	if partSize <= 0 {
		partSize = 4 * 1024 * 1024
	}

	if !hasSourceHashes {
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
		hashData, err = quarkComputeSourceHashes(ctx, source, size)
		if err != nil {
			return drive.Entry{}, err
		}
		hasSourceHashes = true
	}
	if hasSourceHashes && !resumedSession {
		finished, finalFid, err := d.updateUploadHash(ctx, preResp.Data.TaskID, preResp.Data.Fid, preResp.Data.ObjKey, hashData, name, size, putStart)
		if err != nil {
			return drive.Entry{}, err
		}
		if finished {
			drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
			d.deleteUploadSession(sessionKey)
			return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: size, ModTime: mtime, CreatedAt: mtime, UpdatedAt: mtime}, nil
		}
		session = uploadSessionFromPre(sessionKey, parentID, name, size, hashData, preResp, partSize)
	} else if resumedSession && session.Etags == nil {
		session.Etags = map[int]string{}
	}

	sourceFile, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("quark: upload source open: %w", err)
	}
	defer sourceFile.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)

	etagsByPart := map[int]string{}
	if resumedSession {
		for part, etag := range session.Etags {
			etagsByPart[part] = etag
		}
	}
	var totalRead int64
	var submittedParts int
	var completedParts int
	savePart := func(partNumber int, etag string) {
		etagsByPart[partNumber] = etag
		if sessionKey != "" {
			if session.Etags == nil {
				session.Etags = map[int]string{}
			}
			session.Etags[partNumber] = etag
			d.saveUploadSession(session)
		}
	}
	totalParts := int((size + int64(partSize) - 1) / int64(partSize))
	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		offset := int64(partNumber-1) * int64(partSize)
		length := int64(partSize)
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		if length < 0 {
			length = 0
		}
		if _, ok := etagsByPart[partNumber]; ok {
			drive.ReportUploadProgress(req.Progress, length)
		} else {
			logging.L.Debugf("[QUARK] upload part start name=%q task=%q part=%d bytes=%d", name, preResp.Data.TaskID, partNumber, length)
			partReader := func() (io.Reader, error) {
				return io.NewSectionReader(sourceFile, offset, length), nil
			}
			etag, err := d.uploadPart(ctx, &preResp, partNumber, length, partReader)
			if err != nil {
				d.setUploadDebugError(preResp.Data.TaskID, err)
				logging.L.Warnf("[QUARK] upload part failed name=%q task=%q part=%d bytes=%d err=%v", name, preResp.Data.TaskID, partNumber, length, err)
				return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, fmt.Errorf("quark: upload part %d: %w", partNumber, err))
			}
			drive.ReportUploadProgress(req.Progress, length)
			submittedParts++
			completedParts++
			savePart(partNumber, etag)
			logging.L.Debugf("[QUARK] upload part complete name=%q task=%q part=%d etag=%q", name, preResp.Data.TaskID, partNumber, etag)
		}
		totalRead += length
		d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) {
			item.Stage = "uploading_parts"
			item.BytesRead = totalRead
			item.PartsSubmitted = submittedParts
			item.PartsCompleted = completedParts
		})
	}

	etags := make([]string, 0, totalParts)
	for i := 1; i <= totalParts; i++ {
		etags = append(etags, etagsByPart[i])
	}
	if totalRead == 0 {
		logging.L.Debugf("[QUARK] upload empty part start name=%q task=%q", name, preResp.Data.TaskID)
		etag, err := d.uploadPart(ctx, &preResp, 1, 0, func() (io.Reader, error) {
			return strings.NewReader(""), nil
		})
		if err != nil {
			d.setUploadDebugError(preResp.Data.TaskID, err)
			logging.L.Warnf("[QUARK] upload empty part failed name=%q task=%q err=%v", name, preResp.Data.TaskID, err)
			return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, fmt.Errorf("quark: upload part 1: %w", err))
		}
		etags = append(etags, etag)
		savePart(1, etag)
	}
	d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) { item.Stage = "oss_complete" })
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if err := d.ossComplete(ctx, &preResp, etags); err != nil {
		d.setUploadDebugError(preResp.Data.TaskID, err)
		logging.L.Warnf("[QUARK] upload complete multipart failed name=%q task=%q parts=%d err=%v", name, preResp.Data.TaskID, len(etags), err)
		return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, fmt.Errorf("quark: upload complete: %w", err))
	}
	d.updateUploadDebug(preResp.Data.TaskID, func(item *quarkUploadDebug) { item.Stage = "finish" })
	finalFid, err := d.uploadFinish(ctx, preResp.Data.Fid, preResp.Data.ObjKey, preResp.Data.TaskID)
	if err != nil {
		d.setUploadDebugError(preResp.Data.TaskID, err)
		logging.L.Warnf("[QUARK] upload finish failed name=%q task=%q fid=%q err=%v", name, preResp.Data.TaskID, preResp.Data.Fid, err)
		return drive.Entry{}, fmt.Errorf("quark: upload finish: %w", err)
	}
	logging.L.InfofEvery("quark.upload_complete", time.Second, "[QUARK] upload complete name=%q fid=%q size=%d parts=%d dur=%s", name, finalFid, totalRead, len(etags), time.Since(putStart))
	d.deleteUploadSession(sessionKey)
	return drive.Entry{ID: finalFid, ParentID: parentID, Name: name, Size: totalRead, ModTime: mtime, CreatedAt: mtime, UpdatedAt: mtime}, nil
}

func quarkSourceHashes(source drive.ReadOnlyFileSource) (map[string]any, bool, error) {
	md5Sum, ok := drive.SourceHash(source, drive.HashMD5)
	if !ok {
		return nil, false, nil
	}
	sha1Sum, ok := drive.SourceHash(source, drive.HashSHA1)
	if !ok {
		return nil, false, nil
	}
	if len(md5Sum) != md5.Size {
		return nil, false, drive.NonRetryable(fmt.Errorf("quark: source MD5 metadata has %d bytes, want %d", len(md5Sum), md5.Size))
	}
	if len(sha1Sum) != sha1.Size {
		return nil, false, drive.NonRetryable(fmt.Errorf("quark: source SHA-1 metadata has %d bytes, want %d", len(sha1Sum), sha1.Size))
	}
	return map[string]any{
		"md5":  fmt.Sprintf("%X", md5Sum),
		"sha1": fmt.Sprintf("%X", sha1Sum),
	}, true, nil
}

func quarkComputeSourceHashes(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (map[string]any, error) {
	f, err := source.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("quark: upload hash open: %w", err)
	}
	defer f.Close()
	md5Hash := md5.New()
	sha1Hash := sha1.New()
	n, err := io.Copy(io.MultiWriter(md5Hash, sha1Hash), f)
	if err != nil {
		return nil, fmt.Errorf("quark: upload hash: %w", err)
	}
	if n != size {
		return nil, fmt.Errorf("quark: upload hash size mismatch: hashed %d, expected %d", n, size)
	}
	return map[string]any{
		"md5":  fmt.Sprintf("%X", md5Hash.Sum(nil)),
		"sha1": fmt.Sprintf("%X", sha1Hash.Sum(nil)),
	}, nil
}

func (d *Driver) updateUploadHash(ctx context.Context, taskID, fid, objKey string, hashData map[string]any, name string, size int64, startedAt time.Time) (bool, string, error) {
	hashData["task_id"] = taskID
	var hashResp hashResp
	d.updateUploadDebug(taskID, func(item *quarkUploadDebug) { item.Stage = "hash_update" })
	if err := d.cl.request(ctx, http.MethodPost, "/file/update/hash", nil, hashData, &hashResp); err != nil {
		d.setUploadDebugError(taskID, err)
		logging.L.Warnf("[QUARK] upload hash update failed name=%q task=%q size=%d err=%v", name, taskID, size, err)
		return false, "", fmt.Errorf("quark: upload hash: %w", err)
	}
	logging.L.InfofEvery("quark.upload_hash_ok", time.Second, "[QUARK] upload hash update ok name=%q task=%q size=%d finish=%t", name, taskID, size, hashResp.Data.Finish)
	if !hashResp.Data.Finish {
		return false, "", nil
	}
	if hashResp.Data.Fid != "" {
		fid = hashResp.Data.Fid
	}
	finalFid, err := d.uploadFinish(ctx, fid, objKey, taskID)
	if err != nil {
		d.setUploadDebugError(taskID, err)
		logging.L.Warnf("[QUARK] hash-finished upload finish failed name=%q task=%q fid=%q err=%v", name, taskID, fid, err)
		return false, "", fmt.Errorf("quark: upload finish: %w", err)
	}
	logging.L.InfofEvery("quark.hash_finished_upload_complete", time.Second, "[QUARK] hash-finished upload complete name=%q fid=%q size=%d dur=%s", name, finalFid, size, time.Since(startedAt))
	d.debugMu.Lock()
	d.instantUploadCount++
	d.debugMu.Unlock()
	return true, finalFid, nil
}

func (d *Driver) uploadFinish(ctx context.Context, fid, objKey, taskID string) (string, error) {
	var resp struct {
		respEnvelope
		Data struct {
			Fid string `json:"fid"`
		} `json:"data"`
	}
	if err := d.cl.request(ctx, http.MethodPost, "/file/upload/finish", nil, map[string]any{
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

func (d *Driver) ossComplete(ctx context.Context, pre *upPreResp, etags []string) error {
	if len(etags) == 0 {
		return nil
	}
	var xmlBody strings.Builder
	xmlBody.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUpload>`)
	for i, etag := range etags {
		fmt.Fprintf(&xmlBody, `
<Part>
<PartNumber>%d</PartNumber>
<ETag>%s</ETag>
</Part>`, i+1, etag)
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
		err := d.cl.request(ctx, http.MethodPost, "/file/upload/auth", nil, map[string]any{
			"auth_info": pre.Data.AuthInfo,
			"auth_meta": authMeta,
			"task_id":   pre.Data.TaskID,
		}, &authResp)
		if err != nil {
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.oss_complete_auth_retry", time.Second, "[QUARK] oss complete auth failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return err
				}
				continue
			}
			logging.L.Warnf("[QUARK] oss complete auth failed task=%q attempts=%d err=%v", pre.Data.TaskID, attempt+1, err)
			return err
		}

		reqCtx, cancel := context.WithTimeout(ctx, ossRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, ossURL(pre)+"?uploadId="+pre.Data.UploadID, strings.NewReader(body))
		if err != nil {
			cancel()
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
		start := time.Now()
		resp, err := d.cl.ossClient.Do(req)
		cancel()
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "oss_complete",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"headers": util.HeaderKeys(req.Header)},
			Error:     errorString(err),
		})
		if err != nil {
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.oss_complete_http_retry", time.Second, "[QUARK] oss complete http failed; retry task=%q attempt=%d err=%v", pre.Data.TaskID, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return err
				}
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
			logging.L.WarnfEvery("quark.oss_complete_status_retry", time.Second, "[QUARK] oss complete status retry task=%q attempt=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
			if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
				return err
			}
			continue
		}
		logging.L.Warnf("[QUARK] oss complete status failed task=%q attempts=%d status=%d", pre.Data.TaskID, attempt+1, resp.StatusCode)
		var statusErr error = uploadStatusError{op: "oss complete", status: resp.StatusCode}
		if nonRetryableUploadStatus(resp.StatusCode) {
			statusErr = drive.NonRetryable(statusErr)
		}
		return statusErr
	}
	return nil
}

func (d *Driver) uploadPart(ctx context.Context, pre *upPreResp, partNumber int, length int64, openBody func() (io.Reader, error)) (string, error) {
	// Log the host+path only: the upload URL carries an OSS signature
	// (OSSAccessKeyId/Signature/Expires) that must not reach the log.
	uploadURL := pre.Data.UploadURL
	if i := strings.IndexByte(uploadURL, '?'); i != -1 {
		uploadURL = uploadURL[:i]
	}
	logging.L.DebugfEvery("quark.upload_part.enter", time.Second, "[QUARK] upload part enter task=%q part=%d bytes=%d bucket=%q obj=%q upload_url=%q", pre.Data.TaskID, partNumber, length, pre.Data.Bucket, pre.Data.ObjKey, uploadURL)
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
		err := d.cl.request(ctx, http.MethodPost, "/file/upload/auth", nil, map[string]any{
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
				logging.L.WarnfEvery("quark.upload_part_auth_retry", time.Second, "[QUARK] upload part auth failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
					return "", err
				}
				continue
			}
			logging.L.Warnf("[QUARK] upload part auth failed task=%q part=%d attempts=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
			return "", err
		}

		logging.L.DebugfEvery("quark.upload_part.auth", time.Second, "[QUARK] upload part auth done task=%q part=%d auth=%s", pre.Data.TaskID, partNumber, authDur)
		ossURLStr := ossURL(pre) + "?partNumber=" + strconv.Itoa(partNumber) + "&uploadId=" + pre.Data.UploadID
		logging.L.DebugfEvery("quark.upload_part.oss_start", time.Second, "[QUARK] upload part oss put start task=%q part=%d url=%q", pre.Data.TaskID, partNumber, ossURLStr)
		ossStart := time.Now()
		bodyReader, err := openBody()
		if err != nil {
			return "", err
		}
		body := d.limiter.LimitUpload(ctx, bodyReader)
		reqCtx, cancel := context.WithTimeout(ctx, ossRequestTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, ossURLStr, body)
		if err != nil {
			cancel()
			return "", err
		}
		req.ContentLength = length
		req.Header.Set("Authorization", authResp.Data.AuthKey)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("x-oss-date", dateStr)
		req.Header.Set("x-oss-user-agent", "aliyun-sdk-js/6.6.1 Chrome 98.0.4758.80 on Windows 10 64-bit")
		req.Header.Set("Referer", defaultReferer)
		resp, err := d.cl.ossClient.Do(req)
		cancel()
		ossDur := time.Since(ossStart)
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "oss_upload_part",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  ossDur.String(),
			Request:   map[string]any{"part_number": partNumber, "bytes": length, "headers": util.HeaderKeys(req.Header)},
			Error:     errorString(err),
		})
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < ossMaxRetries {
				logging.L.WarnfEvery("quark.upload_part_http_retry", time.Second, "[QUARK] upload part http failed; retry task=%q part=%d attempt=%d err=%v", pre.Data.TaskID, partNumber, attempt+1, err)
				if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
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
			logging.L.DebugfEvery("quark.upload_part.done", time.Second, "[QUARK] upload part done task=%q part=%d bytes=%d auth=%s oss=%s", pre.Data.TaskID, partNumber, length, authDur, ossDur)
			return etag, nil
		}
		if attempt < ossMaxRetries {
			logging.L.WarnfEvery("quark.upload_part_status_retry", time.Second, "[QUARK] upload part status retry task=%q part=%d attempt=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
			if err := sleepContext(ctx, ossRetryDelay(attempt)); err != nil {
				return "", err
			}
			continue
		}
		logging.L.Warnf("[QUARK] upload part status failed task=%q part=%d attempts=%d status=%d", pre.Data.TaskID, partNumber, attempt+1, resp.StatusCode)
		var statusErr error = uploadStatusError{op: fmt.Sprintf("upload part %d", partNumber), status: resp.StatusCode}
		if nonRetryableUploadStatus(resp.StatusCode) {
			statusErr = drive.NonRetryable(statusErr)
		}
		return "", statusErr
	}
	return "", nil
}

type uploadStatusError struct {
	op     string
	status int
}

func (e uploadStatusError) Error() string {
	return fmt.Sprintf("%s status %d", e.op, e.status)
}

func nonRetryableUploadStatus(status int) bool {
	return status >= 400 &&
		status < 500 &&
		status != http.StatusRequestTimeout &&
		status != http.StatusTooManyRequests &&
		status != http.StatusConflict
}

func invalidResumedUploadSession(err error) bool {
	var statusErr uploadStatusError
	return errors.As(err, &statusErr) && statusErr.status == http.StatusConflict
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

func ossRetryDelay(attempt int) time.Duration {
	return retry.ExponentialBackoffWithOptions(attempt, ossRetryBaseDelay, 2*time.Minute, false)
}

func apiError(resp respEnvelope) error {
	if resp.Status < 400 && resp.Code == 0 {
		return nil
	}
	switch resp.Code {
	case 23001:
		return fmt.Errorf("%w: quark: not found", drive.ErrNotFound)
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
	logging.L.DebugfEvery("quark.read_body", time.Second, "[QUARK] ReadBody fid=%q offset=%d size=%d read=%d err=%v dur=%s", r.fid, r.offset, r.size, r.read, err, time.Since(r.start))
	return err
}

func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
