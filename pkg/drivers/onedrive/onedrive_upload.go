// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

var errUploadSessionGone = errors.New("onedrive: upload session gone")

// oneDriveToken 是绑定中保存的 provider 侧上传引用。uploadUrl 是 bearer
// 凭证：绑定文件 0600，commit 或 cancel 后立即删除记录。
type oneDriveToken struct {
	ParentID  string `json:"parent_id"`
	Name      string `json:"name"`
	UploadURL string `json:"upload_url"`
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID := d.resolveID(req.ParentID)
	// 内容指纹只取调用方已提供的哈希，不重读源：onedrive 源可能是远端或
	// 加密流，缺指纹时降级为每次新会话（仍正确，只是不可恢复）。
	var sha256Hex string
	if sum, ok := drive.SourceHash(req.Source, drive.HashSHA256); ok {
		if len(sum) != sha256.Size {
			return drive.Entry{}, drive.NonRetryable(fmt.Errorf("onedrive: source SHA-256 metadata has %d bytes, want %d", len(sum), sha256.Size))
		}
		sha256Hex = hex.EncodeToString(sum)
	}
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: put source open: %w", err)
	}
	defer body.Close()
	if req.Source.Size() <= oneDriveSmallUploadLimit {
		return d.putSmall(ctx, parentID, req.Name, req.Source.Size(), body, req.Progress)
	}
	return d.putLarge(ctx, parentID, req.Name, req.Source.Size(), body, sha256Hex, req.Progress)
}

func (d *Driver) putSmall(ctx context.Context, parentID, name string, size int64, body io.Reader, progress drive.UploadProgress) (drive.Entry, error) {
	var uploadBody = drive.NewUploadProgressReader(progress, body)
	if d.limiter != nil {
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
	}
	var item itemResp
	path := d.apiPath(fmt.Sprintf("/items/%s:/%s:/content", url.PathEscape(parentID), escapePathSegment(name)))
	if err := d.requestRaw(ctx, http.MethodPut, path, uploadBody, "application/octet-stream", &item); err != nil {
		err = fmt.Errorf("onedrive: put %q: %w", name, err)
		if nonRetryableUploadError(err) {
			err = drive.NonRetryable(err)
		}
		return drive.Entry{}, err
	}
	if item.Size == 0 {
		item.Size = size
	}
	return item.entry(parentID), nil
}

// putLarge 大文件分片上传。恢复只发生在内容指纹一致时且 provider 会话仍有效：
// 已传范围用 GET uploadUrl 的 nextExpectedRanges 重建，本地绑定只保存
// uploadUrl，任何分片进度都不落盘。commit 成功即成功，清理不阻塞返回。
func (d *Driver) putLarge(ctx context.Context, parentID, name string, size int64, body drive.ReadOnlyFile, sha256Hex string, progress drive.UploadProgress) (drive.Entry, error) {
	identity := session.Identity{ParentID: parentID, Name: name, Size: size, Fingerprint: sha256Hex}
	sessionKey := identity.Key()

	uploadURL, resumeFrom, err := d.resumeUploadSession(ctx, identity, sessionKey)
	if err != nil {
		return drive.Entry{}, err
	}
	if uploadURL == "" {
		if d.sessions != nil {
			// 预留绑定：先落盘再建 provider 会话。崩溃也只会留下空 token 绑定，
			// 下次尝试按无效记录作废重来，不会产生本地无记录的孤儿会话。
			reserve, _ := json.Marshal(oneDriveToken{ParentID: parentID, Name: name})
			if err := d.sessions.Create(sessionKey, reserve); err != nil {
				return drive.Entry{}, fmt.Errorf("onedrive: persist upload session: %w", err)
			}
		}
		var created createUploadSessionResp
		sessionPath := d.apiPath(fmt.Sprintf("/items/%s:/%s:/createUploadSession", url.PathEscape(parentID), escapePathSegment(name)))
		payload := map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"}}
		if err := d.requestJSON(ctx, http.MethodPost, sessionPath, payload, &created); err != nil {
			if d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q: %w", name, err)
		}
		if created.UploadURL == "" {
			if d.sessions != nil {
				d.sessions.Delete(sessionKey)
			}
			return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q returned empty uploadUrl", name)
		}
		uploadURL = created.UploadURL
		if d.sessions != nil {
			token, _ := json.Marshal(oneDriveToken{ParentID: parentID, Name: name, UploadURL: uploadURL})
			if err := d.sessions.Create(sessionKey, token); err != nil {
				_ = d.cancelUploadSession(context.Background(), uploadURL)
				return drive.Entry{}, fmt.Errorf("onedrive: persist upload session: %w", err)
			}
		}
	}
	// 服务端范围可能只到分片中部（中断尾巴），回退到分片边界重传整个分片。
	resumeFrom = (resumeFrom / d.chunkSize) * d.chunkSize
	drive.ReportUploadProgress(progress, resumeFrom)
	for offset := resumeFrom; offset < size; offset += d.chunkSize {
		if err := ctx.Err(); err != nil {
			return drive.Entry{}, err
		}
		partSize := d.chunkSize
		if remaining := size - offset; remaining < partSize {
			partSize = remaining
		}
		reader := io.NewSectionReader(body, offset, partSize)
		var uploadBody = drive.NewUploadProgressReader(progress, reader)
		if d.limiter != nil {
			uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, uploadBody)
		if err != nil {
			return drive.Entry{}, err
		}
		req.ContentLength = partSize
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+partSize-1, size))
		start := time.Now()
		resp, err := d.client.Do(req)
		d.recordHTTP(ctx, "UploadPart", http.MethodPut, "upload_session", start, respStatus(resp), err)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("onedrive: upload part: %w", err)
		}
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			err := drive.HTTPError("onedrive: upload part", nil, resp, data)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
				err = drive.NonRetryable(err)
			}
			return drive.Entry{}, err
		}
		resp.Body.Close()
		if d.sessions != nil {
			d.sessions.Touch(sessionKey)
		}
	}
	item, err := d.itemByChildName(ctx, parentID, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: resolve uploaded file %q: %w", name, err)
	}
	// commit 成功（文件已可解析）：清理绑定尽力而为，残留由过期回收兜底。
	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return item.entry(parentID), nil
}

// resumeUploadSession 尝试恢复已建 upload session。返回 (uploadURL, 下一偏移)；
// 返回空 uploadURL 表示需要新建会话。
func (d *Driver) resumeUploadSession(ctx context.Context, identity session.Identity, sessionKey string) (string, int64, error) {
	if d.sessions == nil || !identity.Resumable() {
		return "", 0, nil
	}
	binding, ok := d.sessions.Get(sessionKey)
	if !ok {
		return "", 0, nil
	}
	var tok oneDriveToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil || tok.UploadURL == "" {
		d.sessions.Delete(sessionKey)
		return "", 0, nil
	}
	resumeFrom, err := d.uploadSessionResumeOffset(ctx, tok.UploadURL)
	if err != nil {
		if errors.Is(err, errUploadSessionGone) {
			// 会话已过期/完成：取消记录，下次新建。
			d.sessions.Delete(sessionKey)
			_ = d.cancelUploadSession(context.Background(), tok.UploadURL)
			return "", 0, nil
		}
		return "", 0, err
	}
	return tok.UploadURL, resumeFrom, nil
}

type uploadSessionStatusResp struct {
	NextExpectedRanges []string `json:"nextExpectedRanges"`
}

// uploadSessionResumeOffset 查询服务端已传范围，返回下一个待传字节偏移。
func (d *Driver) uploadSessionResumeOffset(ctx context.Context, uploadURL string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uploadURL, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, "UploadSession", http.MethodGet, "upload_session", start, respStatus(resp), err)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return 0, errUploadSessionGone
		}
		data, _ := io.ReadAll(resp.Body)
		return 0, drive.HTTPError("onedrive: query upload session", nil, resp, data)
	}
	var status uploadSessionStatusResp
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return 0, fmt.Errorf("onedrive: decode upload session status: %w", err)
	}
	return nextExpectedOffset(status.NextExpectedRanges), nil
}

// nextExpectedOffset 从 Graph 返回的 nextExpectedRanges（如 "bytes 0-262143/1000000"
// 或 "bytes 262144-/1000000"）计算下一个未传偏移：取所有已传区间的下一个字节的最小值。
func nextExpectedOffset(ranges []string) int64 {
	next := int64(-1)
	for _, r := range ranges {
		var lo, hi int64
		if n, _ := fmt.Sscanf(r, "bytes %d-%d", &lo, &hi); n == 2 {
			if candidate := hi + 1; next < 0 || candidate < next {
				next = candidate
			}
			continue
		}
		var loOnly int64
		if n, _ := fmt.Sscanf(r, "bytes %d-", &loOnly); n == 1 {
			if next < 0 || loOnly < next {
				next = loOnly
			}
		}
	}
	if next < 0 {
		return 0
	}
	return next
}

// cancelUploadSession 幂等取消一个 upload session；404/410 视为已不存在。
func (d *Driver) cancelUploadSession(ctx context.Context, uploadURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, uploadURL, nil)
	if err != nil {
		return err
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, "CancelSession", http.MethodDelete, "upload_session", start, respStatus(resp), err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		data, _ := io.ReadAll(resp.Body)
		return drive.HTTPError("onedrive: cancel upload session", nil, resp, data)
	}
	return nil
}

// expireUploadSessions 回收超过 maxAge 未活动的绑定对应的 provider upload session。
func (d *Driver) expireUploadSessions() {
	if d.sessions == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.sessions.Expire(oneDriveSessionMaxAge, time.Now(), func(binding session.Session) error {
		return d.reclaimUploadSession(ctx, binding)
	})
}

// reclaimUploadSession 幂等回收一个过期绑定；invalid token 没有可回收资源。
func (d *Driver) reclaimUploadSession(ctx context.Context, binding session.Session) error {
	var tok oneDriveToken
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		return nil
	}
	if tok.UploadURL == "" {
		return nil
	}
	return d.cancelUploadSession(ctx, tok.UploadURL)
}
