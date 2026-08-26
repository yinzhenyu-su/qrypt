package aliyundrive

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	now := time.Now()
	size := source.Size()
	parentID = d.resolveID(parentID)
	partCount := int(math.Ceil(float64(size) / float64(d.partSize)))
	if partCount == 0 {
		partCount = 1
	}
	partInfo := make([]map[string]int, 0, partCount)
	for i := 1; i <= partCount; i++ {
		partInfo = append(partInfo, map[string]int{"part_number": i})
	}
	var create createResp
	body := map[string]any{
		"check_name_mode": "overwrite",
		"drive_id":        d.driveID,
		"name":            name,
		"parent_file_id":  parentID,
		"part_info_list":  partInfo,
		"size":            size,
		"type":            "file",
	}
	sessionKey := ""
	resumed := false
	var tok aliyunToken
	var skipParts map[int]bool
	partSize := d.partSize
	// When source provides SHA1 (e.g. from crypt ContentDedupCrypt),
	// skip two-phase pre_hash negotiation: saves one API round trip
	// and avoids re-encrypting the full source on every Open().
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	if sha1sum, ok := d.sourceSHA1(source); ok {
		// 内容指纹（SHA1）参与寻址：内容变化 ⇒ Key 变化 ⇒ 旧分片绝不复用。
		sessionKey = session.Identity{ParentID: parentID, Name: name, Size: size, Fingerprint: sha1sum}.Key()
		body["content_hash"] = sha1sum
		body["content_hash_name"] = "sha1"
		body["proof_version"] = "v1"
		if d.sessions != nil {
			if binding, ok := d.sessions.Get(sessionKey); ok {
				if err := json.Unmarshal(binding.Token, &tok); err == nil && tok.FileID != "" && tok.UploadID != "" {
					resumed = true
					if tok.PartSize > 0 {
						partSize = tok.PartSize
					}
				} else {
					d.sessions.Delete(sessionKey)
				}
			}
		}
		if !resumed {
			proofCode, err := d.proofCode(ctx, source, size)
			if err != nil {
				return drive.Entry{}, err
			}
			body["proof_code"] = proofCode
		}
	} else {
		if preHash, err := fileHeadSHA1(ctx, source, 1024); err == nil {
			body["pre_hash"] = preHash
		} else {
			body["content_hash_name"] = "none"
			body["proof_version"] = "v1"
		}
	}
	var err error
	if resumed {
		// 恢复：分片进度来自本地确认位图（实网验证本网关无分片查询接口：
		// v2 与 v1.0/openFile 的 getUploadUrl/listUploadedParts 均 404），
		// 上传 URL 复用 createWithFolders 下发的预签名 URL；恢复只基于
		// 内容寻址 Key 相同的确认记录（幂等重传兜底）。
		if len(tok.PartURLs) == 0 || tok.UploadID == "" {
			d.sessions.Delete(sessionKey)
			resumed = false
		} else {
			create = tok.createResp(name, size)
			skipParts = session.ConfirmedParts(tok.Confirmed)
		}
	}
	if !resumed {
		// 预留绑定：createWithFolders 是 provider 上传资源创建，先落盘再调用，
		// 崩溃只留下空句柄绑定（下次作废重来），不会产生无记录的孤儿。
		if sessionKey != "" && d.sessions != nil {
			if raw, err := json.Marshal(tok); err != nil {
				return drive.Entry{}, fmt.Errorf("aliyundrive: encode upload session: %w", err)
			} else if err := d.sessions.Create(sessionKey, raw); err != nil {
				return drive.Entry{}, fmt.Errorf("aliyundrive: persist upload session: %w", err)
			}
		}
		err = d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &create)
		var apiErr *apiStatusError
		if errors.As(err, &apiErr) && apiErr.code == "PreHashMatched" {
			delete(body, "pre_hash")
			instantFields, instantErr := d.instantUploadFields(ctx, source, size)
			if instantErr != nil {
				return drive.Entry{}, instantErr
			}
			for key, value := range instantFields {
				body[key] = value
			}
			err = d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &create)
		}
		if err != nil {
			if sessionKey != "" && d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, classifyAliyunUploadError(fmt.Errorf("aliyundrive: upload create: %w", err))
		}
		if create.InstantUpload {
			drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
			d.debugMu.Lock()
			d.instantUploadCount++
			d.debugMu.Unlock()
			if sessionKey != "" && d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			createdAt, updatedAt, modTime := responseTimes(create.UpdatedAt, create.CreatedAt, now)
			return drive.Entry{ID: create.FileID, ParentID: parentID, Name: name, Size: size, ModTime: modTime, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
		}
		if sessionKey != "" && d.sessions != nil {
			tok = aliyunToken{
				FileID:    create.FileID,
				UploadID:  create.UploadID,
				PartSize:  partSize,
				PartURLs:  append([]uploadPartInfo(nil), create.PartInfoList...),
				Confirmed: session.ConfirmedBitmap(size, partSize, nil),
			}
			if raw, err := json.Marshal(tok); err != nil {
				return drive.Entry{}, fmt.Errorf("aliyundrive: encode upload session: %w", err)
			} else if err := d.sessions.Create(sessionKey, raw); err != nil {
				// 持句柄落盘失败：provider 会话无 abort 端点，靠服务端过期；
				// 下次尝试发现空/旧句柄时视为作废重来。
				return drive.Entry{}, fmt.Errorf("aliyundrive: persist upload session: %w", err)
			}
		}
	}
	uploadPartSize := partSize
	if err := d.uploadParts(ctx, source, req.Progress, create.PartInfoList, uploadPartSize, skipParts, func(partNumber int) {
		if sessionKey == "" || d.sessions == nil {
			return
		}
		// 确认记录在 Index 锁内原地更新、节流落盘（≤1 次/分钟）；
		// 崩溃最多丢一分钟确认，对应分片重传幂等覆盖，安全。
		d.sessions.TouchWith(sessionKey, func(s *session.Session) {
			var tok aliyunToken
			// 只对持有效句柄的绑定确认进度：空句柄（并发预留/崩溃残留）的
			// token 不叠加确认位，避免"空句柄 + 已确认"的混合状态。
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
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	var complete completeResp
	completeBody := map[string]any{
		"drive_id":  d.driveID,
		"file_id":   create.FileID,
		"upload_id": create.UploadID,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/file/complete", completeBody, &complete); err != nil {
		if sessionKey != "" && d.sessions != nil && invalidResumedUploadSession(err) {
			d.sessions.Delete(sessionKey)
		}
		return drive.Entry{}, classifyAliyunUploadError(fmt.Errorf("aliyundrive: upload complete: %w", err))
	}
	createdAt, updatedAt, modTime := responseTimes(complete.UpdatedAt, complete.CreatedAt, responseModTime(create.UpdatedAt, create.CreatedAt, now))
	entry := drive.Entry{ID: create.FileID, ParentID: parentID, Name: name, Size: size, ModTime: modTime, CreatedAt: createdAt, UpdatedAt: updatedAt}
	if complete.FileID != "" {
		entry.ID = complete.FileID
	}
	if complete.Name != "" {
		entry.Name = complete.Name
	}
	if complete.Size > 0 {
		entry.Size = complete.Size
	}
	// provider commit 成功即成功：绑定清理尽力而为，残留由过期回收兜底。
	if sessionKey != "" && d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return entry, nil
}

func (d *Driver) instantUploadFields(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (map[string]any, error) {
	contentHash, err := fileSHA1(ctx, source)
	if err != nil {
		return nil, err
	}
	proofCode, err := d.proofCode(ctx, source, size)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content_hash":      contentHash,
		"content_hash_name": "sha1",
		"proof_code":        proofCode,
		"proof_version":     "v1",
	}, nil
}

func (d *Driver) proofCode(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if size <= 0 {
		return "", nil
	}
	accessToken := d.cl.currentAccessToken()
	sum := md5.Sum([]byte(accessToken))
	offsetSeed, ok := new(big.Int).SetString(hex.EncodeToString(sum[:])[:16], 16)
	if !ok {
		return "", fmt.Errorf("aliyundrive: calculate proof offset")
	}
	offset := new(big.Int).Mod(offsetSeed, big.NewInt(size)).Int64()
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: proof open: %w", err)
	}
	defer file.Close()
	buf := make([]byte, 8)
	n, err := file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("aliyundrive: proof read: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, parts []uploadPartInfo, partSize int64, completed map[int]bool, markComplete func(int)) error {
	file, err := source.Open(ctx)
	if err != nil {
		return fmt.Errorf("aliyundrive: upload open: %w", err)
	}
	defer file.Close()
	size := source.Size()
	for _, part := range parts {
		if part.UploadURL == "" {
			return drive.NonRetryable(fmt.Errorf("aliyundrive: upload part %d has empty url", part.PartNumber))
		}
		offset := int64(part.PartNumber-1) * partSize
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		if length < 0 {
			length = 0
		}
		if completed[part.PartNumber] {
			drive.ReportUploadProgress(progress, length)
			continue
		}
		reader := drive.NewUploadProgressReader(progress, io.NewSectionReader(file, offset, length))
		body := d.limiter.LimitUpload(ctx, reader)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.UploadURL, body)
		if err != nil {
			return err
		}
		req.ContentLength = length
		start := time.Now()
		resp, err := d.cl.httpClient.Do(req)
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "upload_part",
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"part_number": part.PartNumber, "bytes": length},
			Error:     errorString(err),
		})
		if err != nil {
			return fmt.Errorf("aliyundrive: upload part %d: %w", part.PartNumber, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := fmt.Errorf("aliyundrive: upload part %d status %d", part.PartNumber, resp.StatusCode)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
				err = drive.NonRetryable(err)
			}
			return err
		}
		if markComplete != nil {
			markComplete(part.PartNumber)
		}
	}
	return nil
}

func (d *Driver) sourceSHA1(source drive.ReadOnlyFileSource) (string, bool) {
	sum, ok := drive.SourceHash(source, drive.HashSHA1)
	if !ok || len(sum) != sha1.Size {
		return "", false
	}
	return hex.EncodeToString(sum), true
}

func fileHeadSHA1(ctx context.Context, source drive.ReadOnlyFileSource, limit int64) (string, error) {
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: pre hash open: %w", err)
	}
	defer file.Close()
	h := sha1.New()
	if _, err := io.CopyN(h, file, limit); err != nil && err != io.EOF {
		return "", fmt.Errorf("aliyundrive: pre hash read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA1(ctx context.Context, source drive.ReadOnlyFileSource) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		if len(sum) != sha1.Size {
			return "", fmt.Errorf("aliyundrive: source SHA-1 metadata has %d bytes, want %d", len(sum), sha1.Size)
		}
		return hex.EncodeToString(sum), nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("aliyundrive: content hash open: %w", err)
	}
	defer file.Close()
	h := sha1.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("aliyundrive: content hash read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func classifyAliyunUploadError(err error) error {
	if err == nil || drive.IsNonRetryable(err) {
		return err
	}
	var apiErr *apiStatusError
	if errors.As(err, &apiErr) && apiErr.status >= 400 && apiErr.status < 500 && apiErr.status != http.StatusRequestTimeout && apiErr.status != http.StatusTooManyRequests {
		return drive.NonRetryable(err)
	}
	return err
}

func responseModTime(updatedAt, createdAt *time.Time, fallback time.Time) time.Time {
	if updatedAt != nil {
		return *updatedAt
	}
	if createdAt != nil {
		return *createdAt
	}
	return fallback
}

func responseTimes(updatedAt, createdAt *time.Time, fallback time.Time) (time.Time, time.Time, time.Time) {
	var created, updated time.Time
	if createdAt != nil {
		created = *createdAt
	}
	if updatedAt != nil {
		updated = *updatedAt
	}
	return created, updated, responseModTime(updatedAt, createdAt, fallback)
}
