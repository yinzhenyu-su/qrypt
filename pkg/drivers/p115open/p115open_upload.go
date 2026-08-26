// Package p115open implements the 115 cloud drive driver on the official
// 115 open platform API (Bearer access_token + refresh_token).
//
// The refresh token is obtained by authorizing an app on open.115.com (for
// example by scanning a PKCE device-code QR with the 115 app). The driver
// auto-refreshes the access token and persists rotated tokens in the state
// store, so a static refresh_token in the config stays valid across restarts.
package p115open

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := d.resolveID(req.ParentID), req.Name, req.Source
	if source == nil {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("115_open: upload source is nil"))
	}
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115_open: upload source open: %w", err)
	}
	defer body.Close()
	fullSHA1 := ""
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		fullSHA1 = strings.ToUpper(hex.EncodeToString(sum))
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	err = d.recordSDK(ctx, "upload", map[string]any{"parent_id": parentID, "name": name, "size": source.Size()}, func() error {
		return d.uploadSource(ctx, parentID, name, source.Size(), fullSHA1, body, req.Progress)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: upload %q: %v", name, err))
		return drive.Entry{}, fmt.Errorf("115_open: upload: %w", err)
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	entry, err := d.waitUploadedFile(ctx, parentID, name, source)
	if err != nil {
		d.setLastError(err.Error())
		return drive.Entry{}, err
	}
	return entry, nil
}

func (d *Driver) uploadSource(ctx context.Context, parentID, name string, size int64, fullSHA1 string, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	if fullSHA1 == "" {
		var err error
		fullSHA1, err = d.hashAll(body)
		if err != nil {
			return err
		}
	}
	preSHA1, err := d.prefixSHA1(size, body)
	if err != nil {
		return err
	}
	initResp, err := d.uploadInit(ctx, name, size, parentID, fullSHA1, preSHA1, "", "")
	if err != nil {
		return err
	}
	if initResp.Status == 2 {
		d.instantUploads.Add(1)
		return nil
	}
	if initResp.Status == 6 || initResp.Status == 7 || initResp.Status == 8 {
		signVal, err := d.hashSignRange(body, initResp.SignCheck)
		if err != nil {
			return err
		}
		initResp, err = d.uploadInit(ctx, name, size, parentID, fullSHA1, preSHA1, initResp.SignKey, signVal)
		if err != nil {
			return err
		}
		if initResp.Status == 2 {
			d.instantUploads.Add(1)
			return nil
		}
	}
	if initResp.Bucket == "" || initResp.Object == "" {
		return drive.NonRetryable(fmt.Errorf("115_open: upload init missing bucket/object (status=%d)", initResp.Status))
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return d.uploadToOSS(ctx, parentID, name, size, fullSHA1, initResp, body, progress)
}

func (d *Driver) hashAll(body io.ReadSeeker) (string, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.Copy(hasher, body); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

func (d *Driver) prefixSHA1(size int64, body io.ReadSeeker) (string, error) {
	prefixSize := int64(preHashSize)
	if size < prefixSize {
		prefixSize = size
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.CopyN(hasher, body, prefixSize); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

// hashSignRange hashes the byte range [start,end] specified by signCheck
// (format "start-end") for the 115 two-way rapid-upload verification.

func (d *Driver) hashSignRange(body io.ReadSeeker, signCheck string) (string, error) {
	parts := strings.SplitN(signCheck, "-", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	if end < start {
		return "", fmt.Errorf("115_open: bad sign_check %q", signCheck)
	}
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha1.New()
	if _, err := io.CopyN(hasher, body, end-start+1); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hasher.Sum(nil))), nil
}

func (d *Driver) uploadInit(ctx context.Context, name string, size int64, parentID, fullSHA1, preSHA1, signKey, signVal string) (*sdk.UploadInitResp, error) {
	return d.cl.UploadInit(ctx, &sdk.UploadInitReq{
		FileName: name,
		FileSize: size,
		Target:   parentID,
		FileID:   fullSHA1,
		PreID:    preSHA1,
		SignKey:  signKey,
		SignVal:  signVal,
	})
}

func (d *Driver) callbackOf(initResp *sdk.UploadInitResp) (callback, callbackVar string) {
	if initResp == nil || initResp.Callback.Value == nil {
		return "", ""
	}
	return initResp.Callback.Value.Callback, initResp.Callback.Value.CallbackVar
}

func (d *Driver) uploadToOSS(ctx context.Context, parentID, name string, size int64, sha1Hex string, initResp *sdk.UploadInitResp, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	ossToken, err := d.ossTokenFor(ctx)
	if err != nil {
		return fmt.Errorf("115_open: get oss token: %w", err)
	}
	ossClient, err := oss.New(
		ossToken.Endpoint,
		ossToken.AccessKeyId,
		ossToken.AccessKeySecret,
		oss.SecurityToken(ossToken.SecurityToken),
		oss.EnableMD5(true),
		oss.EnableCRC(true),
	)
	if err != nil {
		return fmt.Errorf("115_open: create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(initResp.Bucket)
	if err != nil {
		return fmt.Errorf("115_open: open oss bucket: %w", err)
	}
	if size < calPartSize(size) {
		uploadBody := drive.NewUploadProgressReader(progress, body)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		start := time.Now()
		err := bucket.PutObject(initResp.Object, contextReader{ctx: ctx, reader: uploadBody}, d.ossCallbackOptions(initResp, nil)...)
		d.metrics.Record(ctx, uploadMetric("oss_upload", size, start, err))
		return err
	}
	return d.uploadMultipart(ctx, parentID, name, size, sha1Hex, initResp, bucket, body, progress)
}

func (d *Driver) ossCallbackOptions(initResp *sdk.UploadInitResp, bodyBytes *[]byte) []oss.Option {
	callback, callbackVar := d.callbackOf(initResp)
	options := make([]oss.Option, 0, 3)
	if callback != "" {
		options = append(options, oss.Callback(base64.StdEncoding.EncodeToString([]byte(callback))))
	}
	if callbackVar != "" {
		options = append(options, oss.CallbackVar(base64.StdEncoding.EncodeToString([]byte(callbackVar))))
	}
	if bodyBytes != nil {
		options = append(options, oss.CallbackResult(bodyBytes))
	}
	return options
}

func (d *Driver) uploadMultipart(ctx context.Context, parentID, name string, size int64, sha1Hex string, initResp *sdk.UploadInitResp, bucket *oss.Bucket, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	// 内容指纹（SHA1）参与寻址：内容变化 ⇒ Key 变化 ⇒ 旧分片绝不复用。
	sessionKey := session.Identity{ParentID: parentID, Name: name, Size: size, Fingerprint: strings.ToUpper(sha1Hex)}.Key()
	imur, completed, err := d.beginMultipartUpload(ctx, sessionKey, initResp, bucket, size)
	if err != nil {
		return err
	}
	partSize := calPartSize(size)
	partsByNumber := make(map[int]oss.UploadPart, len(completed))
	for _, part := range completed {
		partsByNumber[part.PartNumber] = part
	}
	uploadParts := make([]oss.UploadPart, 0, len(p115OpenUploadPartRanges(size, partSize)))
	for _, part := range p115OpenUploadPartRanges(size, partSize) {
		if completedPart, ok := partsByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			uploadParts = append(uploadParts, completedPart)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		uploadBody := drive.NewUploadProgressReader(progress, reader)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		uploadBody = contextReader{ctx: ctx, reader: uploadBody}
		start := time.Now()
		uploadedPart, err := bucket.UploadPart(imur, uploadBody, part.Size, part.Number)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		d.metrics.Record(ctx, uploadPartMetric("oss_upload_part", part, start, err))
		if err != nil {
			return fmt.Errorf("115_open: upload part %d: %w", part.Number, err)
		}
		partsByNumber[part.Number] = uploadedPart
		uploadParts = append(uploadParts, uploadedPart)
		if d.sessions != nil {
			d.sessions.Touch(sessionKey)
		}
	}
	sort.Slice(uploadParts, func(i, j int) bool {
		return uploadParts[i].PartNumber < uploadParts[j].PartNumber
	})
	drive.ReportUploadPhase(progress, drive.UploadPhaseCommitting)
	var bodyBytes []byte
	_, err = bucket.CompleteMultipartUpload(imur, uploadParts, d.ossCallbackOptions(initResp, &bodyBytes)...)
	if err != nil {
		// complete 失败但会话已明确失效：幂等回收后下次重建；临时错误保留绑定。
		if d.sessions != nil && invalidOpenUploadSession(err) {
			_ = d.abortOpenUploadSession(bucket, p115OpenToken{Bucket: imur.Bucket, Object: imur.Key, UploadID: imur.UploadID})
			d.sessions.Delete(sessionKey)
		}
		return fmt.Errorf("115_open: complete multipart upload: %w", err)
	}
	// provider commit 成功即成功：绑定清理尽力而为，残留由过期回收兜底。
	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return nil
}

// beginMultipartUpload 返回复用的或新建的 multipart 上传句柄，以及从服务端
// 重建的已完成分片（全新上传为空）。进度真相在服务端（OSS ListParts），
// 查询的临时失败退化为同句柄全量重传（分片按编号幂等覆盖），明确失效则
// 幂等回收后按全新上传处理。
func (d *Driver) beginMultipartUpload(ctx context.Context, sessionKey string, initResp *sdk.UploadInitResp, bucket *oss.Bucket, size int64) (oss.InitiateMultipartUploadResult, []oss.UploadPart, error) {
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(sessionKey); ok {
			var tok p115OpenToken
			if err := json.Unmarshal(binding.Token, &tok); err == nil && tok.UploadID != "" && tok.Bucket != "" && tok.Object != "" {
				parts, err := d.listCompletedParts(ctx, bucket, tok.Object, tok.UploadID)
				if err != nil {
					if !invalidOpenUploadSession(err) {
						// 查询临时失败：同句柄全量重传（分片幂等覆盖），不中断上传。
						logging.L.Warnf("115_open: list parts %q failed, will re-upload all parts: %v", tok.Object, err)
						return oss.InitiateMultipartUploadResult{Bucket: tok.Bucket, Key: tok.Object, UploadID: tok.UploadID}, nil, nil
					}
					// 会话已失效：幂等回收后按全新上传处理。
					_ = d.abortOpenUploadSession(bucket, tok)
					d.sessions.Delete(sessionKey)
				} else {
					return oss.InitiateMultipartUploadResult{Bucket: tok.Bucket, Key: tok.Object, UploadID: tok.UploadID}, parts, nil
				}
			} else {
				// 无效绑定（预留后未完成创建）：作废重来。
				d.sessions.Delete(sessionKey)
			}
		}
	}

	// 预留绑定：句柄落盘后才发起 provider 上传，崩溃只留下可回收的绑定。
	partSize := calPartSize(size)
	token := p115OpenToken{Bucket: initResp.Bucket, Object: initResp.Object, PartSize: partSize}
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115_open: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115_open: persist upload session: %w", err)
		}
	}
	initResult, err := bucket.InitiateMultipartUpload(initResp.Object, oss.Sequential())
	if err != nil {
		if d.sessions != nil {
			d.sessions.Delete(sessionKey)
		}
		return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115_open: initiate multipart upload: %w", err)
	}
	token.UploadID = initResult.UploadID
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115_open: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			// 持 UploadID 落盘失败：立即回收刚创建的 provider 上传，避免孤儿。
			_ = d.abortOpenUploadSession(bucket, token)
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115_open: persist multipart upload id: %w", err)
		}
	}
	return initResult, nil, nil
}

func (d *Driver) ossTokenFor(ctx context.Context) (*sdk.UploadGetTokenResp, error) {
	d.ossMu.Lock()
	defer d.ossMu.Unlock()
	if d.ossToken != nil && time.Since(d.ossTokenAt) < 5*time.Minute {
		return d.ossToken, nil
	}
	token, err := d.cl.UploadGetToken(ctx)
	if err != nil {
		return nil, err
	}
	d.ossToken = token
	d.ossTokenAt = time.Now()
	return token, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err != nil {
		return n, err
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, nil
}

func entryFromFile(f sdk.GetFilesResp_File) drive.Entry {
	return drive.Entry{
		ID:        f.Fid,
		ParentID:  f.Pid,
		Name:      f.Fn,
		Size:      f.FS,
		IsDir:     f.Fc == "0",
		ModTime:   time.Unix(f.Upt, 0),
		UpdatedAt: time.Unix(f.Uet, 0),
		Extra:     f,
	}
}

func uploadMetric(operation string, bytes int64, started time.Time, err error) drive.MetricEvent {
	event := drive.MetricEvent{
		Layer:     "driver.oss",
		Operation: operation,
		Duration:  time.Since(started).String(),
		Request:   map[string]any{"bytes": bytes},
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

func uploadPartMetric(operation string, part p115OpenUploadPartRange, started time.Time, err error) drive.MetricEvent {
	event := drive.MetricEvent{
		Layer:     "driver.oss",
		Operation: operation,
		Duration:  time.Since(started).String(),
		Request: map[string]any{
			"part_number": part.Number,
			"bytes":       part.Size,
			"offset":      part.Offset,
		},
	}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

type p115OpenUploadPartRange struct {
	Number int
	Offset int64
	Size   int64
}

func p115OpenUploadPartRanges(size, partSize int64) []p115OpenUploadPartRange {
	if size < 0 || partSize <= 0 {
		return nil
	}
	if size == 0 {
		return []p115OpenUploadPartRange{{Number: 1}}
	}
	parts := make([]p115OpenUploadPartRange, 0, int((size+partSize-1)/partSize))
	for offset, number := int64(0), 1; offset < size; offset, number = offset+partSize, number+1 {
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		parts = append(parts, p115OpenUploadPartRange{Number: number, Offset: offset, Size: length})
	}
	return parts
}

// calPartSize picks an OSS multipart part size that keeps the total part
// count under the 10000-part limit for very large files.

func calPartSize(fileSize int64) int64 {
	var partSize int64 = 20 * 1024 * 1024
	switch {
	case fileSize > 1<<40: // over 1TB
		partSize = 5 << 30
	case fileSize > 768<<30:
		partSize = 109951163
	case fileSize > 512<<30:
		partSize = 82463373
	case fileSize > 384<<30:
		partSize = 54975582
	case fileSize > 256<<30:
		partSize = 41231687
	case fileSize > 128<<30:
		partSize = 27487791
	}
	return partSize
}

func entrySHA1(entry drive.Entry) string {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		return strings.ToUpper(f.Sha1)
	case *sdk.GetFilesResp_File:
		if f != nil {
			return strings.ToUpper(f.Sha1)
		}
	}
	return ""
}

func rawEntrySize(entry drive.Entry) int64 {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		return f.FS
	case *sdk.GetFilesResp_File:
		if f != nil {
			return f.FS
		}
	}
	return entry.Size
}
