// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	cipher "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := d.resolveID(req.ParentID), req.Name, req.Source
	if source == nil {
		return drive.Entry{}, drive.NonRetryable(fmt.Errorf("115: upload source is nil"))
	}
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("115: upload source open: %w", err)
	}
	defer body.Close()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	err = d.recordSDK(ctx, "upload", map[string]any{"parent_id": parentID, "name": name, "size": source.Size()}, func() error {
		return d.uploadSource(ctx, parentID, name, source.Size(), body, req.Progress)
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: upload %q: %v", name, err))
		return drive.Entry{}, fmt.Errorf("115: upload: %w", err)
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	entry, err := d.waitUploadedFile(ctx, parentID, name, source)
	if err != nil {
		d.setLastError(err.Error())
		return drive.Entry{}, err
	}
	return entry, nil
}

func (d *Driver) uploadSource(ctx context.Context, parentID, name string, size int64, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	ok, err := d.cl.UploadAvailable()
	if err != nil || !ok {
		return err
	}
	if d.cl.UploadMetaInfo != nil && size > d.cl.UploadMetaInfo.SizeLimit {
		return drive.NonRetryable(driver115.ErrUploadTooLarge)
	}
	digest, err := d.cl.GetDigestResult(body)
	if err != nil {
		return err
	}
	fastInfo, err := d.rapidUpload(size, name, parentID, digest.PreID, digest.QuickID, body)
	if err != nil {
		return err
	}
	instant, err := fastInfo.Ok()
	if err != nil {
		return err
	}
	if instant {
		d.instantUploads.Add(1)
		return nil
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if size < p115MultipartMinSize {
		uploadBody := drive.NewUploadProgressReader(progress, body)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		return d.cl.UploadByOSS(&fastInfo.UploadOSSParams, uploadBody, parentID)
	}
	return d.uploadMultipart(ctx, parentID, name, size, digest.QuickID, &fastInfo.UploadOSSParams, body, progress)
}

func (d *Driver) rapidUpload(size int64, name, parentID, preID, sha1ID string, body io.ReadSeeker) (*driver115.UploadInitResp, error) {
	ecdhCipher, err := cipher.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	userID := strconv.FormatInt(d.cl.UserID, 10)
	target := "U_1_" + parentID
	sizeString := strconv.FormatInt(size, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", name)
	form.Set("filesize", sizeString)
	form.Set("fileid", sha1ID)
	form.Set("target", target)
	form.Set("sig", d.cl.GenerateSignature(sha1ID, target))

	var result driver115.UploadInitResp
	signKey, signVal := "", ""
	for retry := true; retry; {
		t := driver115.NowMilli()
		encodedToken, err := ecdhCipher.EncodeToken(t.ToInt64())
		if err != nil {
			return nil, err
		}
		form.Set("t", t.String())
		form.Set("token", uploadToken(userID, sha1ID, preID, t.String(), sizeString, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		encrypted, err := ecdhCipher.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		req := d.cl.NewRequest().
			SetQueryParam("k_ec", encodedToken).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		bodyBytes, readErr := io.ReadAll(data)
		closeErr := data.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		decrypted, err := ecdhCipher.Decrypt(bodyBytes)
		if err != nil {
			return nil, err
		}
		result = driver115.UploadInitResp{}
		if err := driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status != 7 {
			retry = false
			continue
		}
		signKey = result.SignKey
		signVal, err = d.cl.UploadDigestRange(body, result.SignCheck)
		if err != nil {
			return nil, err
		}
	}
	result.SHA1 = sha1ID
	return &result, nil
}

func (d *Driver) uploadMultipart(ctx context.Context, parentID, name string, size int64, sha1Hex string, params *driver115.UploadOSSParams, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	if params == nil || params.Bucket == "" || params.Object == "" {
		return drive.NonRetryable(fmt.Errorf("115: upload oss params missing bucket or object"))
	}
	// 内容指纹（SHA1，ossp 需要）参与寻址：内容变化 ⇒ Key 变化 ⇒ 旧分片绝不复用。
	sessionKey := session.Identity{ParentID: parentID, Name: name, Size: size, Fingerprint: strings.ToUpper(sha1Hex)}.Key()

	ossToken, err := d.cl.GetOSSToken()
	if err != nil {
		return fmt.Errorf("115: get oss token: %w", err)
	}
	bucket, err := d.ossUploadBucket(params.Bucket)
	if err != nil {
		return err
	}
	partSize := int64(p115MultipartPartSize)
	imur, completed, err := d.beginMultipartUpload(ctx, sessionKey, params, partSize, bucket, ossToken)
	if err != nil {
		return err
	}
	partsByNumber := make(map[int]oss.UploadPart, len(completed))
	for _, part := range completed {
		partsByNumber[part.PartNumber] = part
	}
	uploadParts := make([]oss.UploadPart, 0, len(p115UploadPartRanges(size, partSize)))
	for _, part := range p115UploadPartRanges(size, partSize) {
		if completedPart, ok := partsByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			uploadParts = append(uploadParts, completedPart)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Until(ossToken.Expiration) < 5*time.Minute {
			ossToken, err = d.cl.GetOSSToken()
			if err != nil {
				return fmt.Errorf("115: refresh oss token: %w", err)
			}
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		uploadBody := drive.NewUploadProgressReader(progress, reader)
		uploadBody = d.bandwidthLimiter.LimitUpload(ctx, uploadBody)
		uploadBody = contextReader{ctx: ctx, reader: uploadBody}
		start := time.Now()
		uploadedPart, err := bucket.UploadPart(imur, uploadBody, part.Size, part.Number, driver115.OssOption(params, ossToken)...)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
		}
		d.metrics.Record(ctx, uploadPartMetric("oss_upload_part", part, start, err))
		if err != nil {
			return fmt.Errorf("115: upload part %d: %w", part.Number, err)
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
	_, err = bucket.CompleteMultipartUpload(imur, uploadParts,
		append(driver115.OssOption(params, ossToken), oss.CallbackResult(&bodyBytes))...,
	)
	if err != nil {
		// complete 失败但有明确失效（会话不存在/冲突）：幂等回收后下次重建；
		// 其余临时错误保留绑定，下次继续恢复。
		if d.sessions != nil && invalidResumedUploadSession(err) {
			_ = d.abortUploadSession(context.Background(), bucket, p115Token{Bucket: imur.Bucket, Object: imur.Key, UploadID: imur.UploadID})
			d.sessions.Delete(sessionKey)
		}
		return fmt.Errorf("115: complete multipart upload: %w", err)
	}
	var uploadResult driver115.UploadResult
	if err := json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return fmt.Errorf("115: complete multipart response: %w", err)
	}
	if err := uploadResult.Err(string(bodyBytes)); err != nil {
		return fmt.Errorf("115: complete multipart result: %w", err)
	}
	// provider commit 成功即成功：绑定清理尽力而为，残留由过期回收兜底。
	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return nil
}

// beginMultipartUpload 返回复用的或新建的 multipart 上传句柄，以及从服务端
// 重建的已完成分片（全新上传为空）。进度真相在服务端（OSS ListParts），
// 本地只保存句柄；查询的临时失败（网络/解析）退化为同句柄全量重传（分片
// 按编号幂等覆盖），查询/上传明确失效则幂等回收后按全新上传处理。
func (d *Driver) beginMultipartUpload(ctx context.Context, sessionKey string, params *driver115.UploadOSSParams, partSize int64, bucket *oss.Bucket, ossToken *driver115.UploadOSSTokenResp) (oss.InitiateMultipartUploadResult, []oss.UploadPart, error) {
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(sessionKey); ok {
			var tok p115Token
			if err := json.Unmarshal(binding.Token, &tok); err == nil && tok.UploadID != "" && tok.Bucket != "" && tok.Object != "" {
				parts, err := d.listCompletedParts(ctx, bucket, tok.Object, tok.UploadID, ossToken.SecurityToken)
				if err != nil {
					if !invalidResumedUploadSession(err) {
						// 查询临时失败：同句柄全量重传（分片幂等覆盖），不中断上传。
						logging.L.Warnf("115: list parts %q failed, will re-upload all parts: %v", tok.Object, err)
						return oss.InitiateMultipartUploadResult{Bucket: tok.Bucket, Key: tok.Object, UploadID: tok.UploadID}, nil, nil
					}
					// 会话已失效：幂等回收后按全新上传处理。
					_ = d.abortUploadSession(context.Background(), bucket, tok)
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
	token := p115Token{Bucket: params.Bucket, Object: params.Object, PartSize: partSize, Callback: params.Callback.Callback, CallbackV: params.Callback.CallbackVar}
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115: persist upload session: %w", err)
		}
	}
	imur, err := bucket.InitiateMultipartUpload(params.Object,
		oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
		oss.UserAgentHeader(driver115.OSSUserAgent),
		oss.EnableSha1(),
		oss.Sequential(),
	)
	if err != nil {
		if d.sessions != nil {
			d.sessions.Delete(sessionKey)
		}
		return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115: initiate multipart upload: %w", err)
	}
	token.UploadID = imur.UploadID
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			// 持 UploadID 落盘失败：立即回收刚创建的 provider 上传，避免孤儿。
			_ = d.abortUploadSession(context.Background(), bucket, token)
			return oss.InitiateMultipartUploadResult{}, nil, fmt.Errorf("115: persist multipart upload id: %w", err)
		}
	}
	return imur, nil, nil
}

func uploadToken(userID, sha1ID, preID, timestamp, size, signKey, signVal string) string {
	userIDMD5 := md5.Sum([]byte(userID))
	tokenMD5 := md5.Sum([]byte(md5Salt + sha1ID + size + signKey + signVal + userID + timestamp + hex.EncodeToString(userIDMD5[:]) + appVer))
	return hex.EncodeToString(tokenMD5[:])
}

type p115UploadPartRange struct {
	Number int
	Offset int64
	Size   int64
}

func uploadPartMetric(operation string, part p115UploadPartRange, started time.Time, err error) drive.MetricEvent {
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

func p115UploadPartRanges(size, partSize int64) []p115UploadPartRange {
	if size < 0 || partSize <= 0 {
		return nil
	}
	if size == 0 {
		return []p115UploadPartRange{{Number: 1}}
	}
	parts := make([]p115UploadPartRange, 0, int((size+partSize-1)/partSize))
	for offset, number := int64(0), 1; offset < size; offset, number = offset+partSize, number+1 {
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		parts = append(parts, p115UploadPartRange{Number: number, Offset: offset, Size: length})
	}
	return parts
}

func entryFromFile(f driver115.File) drive.Entry {
	modTime := f.ModTime()
	return drive.Entry{
		ID:        f.GetID(),
		ParentID:  f.ParentID,
		Name:      f.GetName(),
		Size:      f.GetSize(),
		IsDir:     f.IsDir(),
		ModTime:   modTime,
		UpdatedAt: modTime,
		Extra:     f,
	}
}

func entrySHA1(entry drive.Entry) string {
	switch f := drive.EntryRawExtra(entry).(type) {
	case driver115.File:
		return strings.ToUpper(f.Sha1)
	case *driver115.File:
		if f != nil {
			return strings.ToUpper(f.Sha1)
		}
	}
	return ""
}

func rawEntrySize(entry drive.Entry) int64 {
	switch f := drive.EntryRawExtra(entry).(type) {
	case driver115.File:
		return f.GetSize()
	case *driver115.File:
		if f != nil {
			return f.GetSize()
		}
	}
	return entry.Size
}

func isPendingDeleteError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "errno\":990009") || strings.Contains(msg, "操作尚未执行完成")
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nosuchupload") ||
		strings.Contains(s, "invalidupload") ||
		strings.Contains(s, "uploadid") ||
		strings.Contains(s, "upload id") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "409")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}
