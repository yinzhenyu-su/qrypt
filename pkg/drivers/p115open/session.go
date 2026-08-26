package p115open

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	p115OpenSessionFile        = "115_open_upload_sessions.json"
	p115OpenSessionMaxAge      = 24 * time.Hour
	p115OpenSessionExpiryEvery = time.Hour
)

// p115OpenToken 是持久化的 provider 上传句柄；分片进度不落盘，
// 用 OSS ListParts 从服务端重建。
type p115OpenToken struct {
	Bucket   string `json:"bucket"`
	Object   string `json:"object"`
	UploadID string `json:"upload_id"`
	PartSize int64  `json:"part_size"`
}

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, p115OpenSessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("115_open: upload session state failed: %v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, p115OpenSessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(p115OpenSessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 幂等释放一个 multipart 上传；已 complete/abort 的
// 会话返回 404（NoSuchUpload）视为已不存在。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok p115OpenToken
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil // 损坏绑定无法回收：直接丢弃
	}
	if tok.UploadID == "" || tok.Bucket == "" {
		return nil
	}
	ossToken, err := d.ossTokenFor(context.Background())
	if err != nil {
		return fmt.Errorf("115_open: reclaim get oss token: %w", err)
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
		return fmt.Errorf("115_open: reclaim create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(tok.Bucket)
	if err != nil {
		return fmt.Errorf("115_open: reclaim open oss bucket: %w", err)
	}
	return d.abortOpenUploadSession(bucket, tok)
}

// abortOpenUploadSession 幂等回收 multipart；已 complete/已 abort 返回
// 404（NoSuchUpload）视为成功。
func (d *Driver) abortOpenUploadSession(bucket *oss.Bucket, tok p115OpenToken) error {
	imur := oss.InitiateMultipartUploadResult{Bucket: tok.Bucket, Key: tok.Object, UploadID: tok.UploadID}
	if err := bucket.AbortMultipartUpload(imur); err != nil && !invalidOpenUploadSession(err) {
		return err
	}
	return nil
}

// listCompletedParts 用 OSS ListParts 从服务端重建已上传分片（本地零分片状态）。
// 查询失败返回错误，由调用方决定按临时故障全量重传。
func (d *Driver) listCompletedParts(ctx context.Context, bucket *oss.Bucket, object, uploadID string) ([]oss.UploadPart, error) {
	var parts []oss.UploadPart
	marker := "0"
	for {
		resp, err := bucket.Client.Conn.DoWithContext(ctx, http.MethodGet, bucket.BucketName, object,
			map[string]any{"uploadId": uploadID, "max-parts": "1000", "part-number-marker": marker},
			nil, nil, 0, nil)
		if err != nil {
			return nil, fmt.Errorf("115_open: list parts: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("115_open: list parts close response: %w", closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("115_open: list parts: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if readErr != nil {
			return nil, fmt.Errorf("115_open: list parts read response: %w", readErr)
		}
		var result oss.ListUploadedPartsResult
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("115_open: list parts response: %w", err)
		}
		for _, part := range result.UploadedParts {
			if part.PartNumber > 0 && part.ETag != "" {
				parts = append(parts, oss.UploadPart{PartNumber: part.PartNumber, ETag: part.ETag})
			}
		}
		if !result.IsTruncated || result.NextPartNumberMarker == "" {
			break
		}
		marker = result.NextPartNumberMarker
	}
	return parts, nil
}

// invalidOpenUploadSession 判断 OSS 错误是否表示上传会话已失效。
func invalidOpenUploadSession(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nosuchupload") ||
		strings.Contains(s, "invalidupload") ||
		strings.Contains(s, "uploadid") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "409")
}
