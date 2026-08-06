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
	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/util/uploadsession"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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
	sessionKey := uploadsession.Key(parentID, name, size, strings.ToUpper(sha1Hex))
	session, resumed := d.loadUploadSession(sessionKey)
	if resumed {
		logging.L.InfofEvery("115.upload_resume", time.Second, "[115] upload resume name=%q upload_id=%q completed_parts=%d", name, session.UploadID, len(session.Parts))
		params = session.uploadParams()
	} else {
		session = p115UploadSession{
			Key:       sessionKey,
			ParentID:  parentID,
			Name:      name,
			Size:      size,
			SHA1:      strings.ToUpper(sha1Hex),
			Bucket:    params.Bucket,
			Object:    params.Object,
			PartSize:  p115MultipartPartSize,
			Callback:  params.Callback.Callback,
			CallbackV: params.Callback.CallbackVar,
		}
	}
	partSize := session.PartSize
	if partSize <= 0 {
		partSize = p115MultipartPartSize
		session.PartSize = partSize
	}
	ossToken, err := d.cl.GetOSSToken()
	if err != nil {
		return fmt.Errorf("115: get oss token: %w", err)
	}
	ossClient, err := oss.New(
		d.cl.GetOSSEndpoint(d.cl.UseInternalUpload),
		ossToken.AccessKeyID,
		ossToken.AccessKeySecret,
		oss.EnableMD5(true),
		oss.EnableCRC(true),
	)
	if err != nil {
		return fmt.Errorf("115: create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(session.Bucket)
	if err != nil {
		return fmt.Errorf("115: open oss bucket: %w", err)
	}
	imur := oss.InitiateMultipartUploadResult{
		Bucket:   session.Bucket,
		Key:      session.Object,
		UploadID: session.UploadID,
	}
	if !resumed || imur.UploadID == "" {
		imur, err = bucket.InitiateMultipartUpload(session.Object,
			oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
			oss.UserAgentHeader(driver115.OSSUserAgent),
			oss.EnableSha1(),
			oss.Sequential(),
		)
		if err != nil {
			return fmt.Errorf("115: initiate multipart upload: %w", err)
		}
		session.UploadID = imur.UploadID
	}
	partsByNumber := session.partsByNumber()
	uploadParts := make([]oss.UploadPart, 0, len(p115UploadPartRanges(size, partSize)))
	for _, part := range p115UploadPartRanges(size, partSize) {
		if completed, ok := partsByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			uploadParts = append(uploadParts, completed)
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
			return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: upload part %d: %w", part.Number, err))
		}
		partsByNumber[part.Number] = uploadedPart
		uploadParts = append(uploadParts, uploadedPart)
		session.Parts = append(session.Parts, ossPart{Number: uploadedPart.PartNumber, ETag: uploadedPart.ETag})
		d.saveUploadSession(session)
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
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart upload: %w", err))
	}
	var uploadResult driver115.UploadResult
	if err := json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart response: %w", err))
	}
	if err := uploadResult.Err(string(bodyBytes)); err != nil {
		return d.resumedUploadSessionError(resumed, sessionKey, fmt.Errorf("115: complete multipart result: %w", err))
	}
	d.deleteUploadSession(sessionKey)
	return nil
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
