// Package p115 implements the 115 cloud drive driver.
package p115

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

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// p115Token is the persisted provider handle for one multipart upload. Part
// progress is never stored locally: it is rebuilt with OSS ListParts.
type p115Token struct {
	Bucket    string `json:"bucket"`
	Object    string `json:"object"`
	UploadID  string `json:"upload_id"`
	PartSize  int64  `json:"part_size"`
	Callback  string `json:"callback,omitempty"`
	CallbackV string `json:"callback_var,omitempty"`
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

func (d *Driver) installSessionIndex(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, p115SessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("115: upload session state failed: %v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireSessions()
	go session.RunExpirer(ctx, p115SessionExpiryEvery, d.expireSessions)
}

func (d *Driver) expireSessions() {
	if d.sessions != nil {
		d.sessions.Expire(p115SessionMaxAge, time.Now(), d.reclaimUploadSession)
	}
}

// reclaimUploadSession 幂等释放一个 multipart 上传；已 complete/abort 的
// 会话返回 404（NoSuchUpload）视为已不存在。
func (d *Driver) reclaimUploadSession(s session.Session) error {
	var tok p115Token
	if err := json.Unmarshal(s.Token, &tok); err != nil {
		return nil // 损坏绑定无法回收：直接丢弃
	}
	if tok.UploadID == "" || tok.Bucket == "" {
		return nil
	}
	bucket, err := d.ossUploadBucket(tok.Bucket)
	if err != nil {
		return fmt.Errorf("115: reclaim open oss bucket %q: %w", tok.Bucket, err)
	}
	return d.abortUploadSession(context.Background(), bucket, tok)
}

// ossUploadBucket 返回用于 multipart 操作的 OSS bucket（获取 STS 令牌并构造
// 客户端）。调用方负责按 driver115.OssOption 携带 SecurityToken。
func (d *Driver) ossUploadBucket(bucketName string) (*oss.Bucket, error) {
	ossToken, err := d.cl.GetOSSToken()
	if err != nil {
		return nil, fmt.Errorf("115: get oss token: %w", err)
	}
	ossClient, err := oss.New(
		d.cl.GetOSSEndpoint(d.cl.UseInternalUpload),
		ossToken.AccessKeyID,
		ossToken.AccessKeySecret,
		oss.EnableMD5(true),
		oss.EnableCRC(true),
	)
	if err != nil {
		return nil, fmt.Errorf("115: create oss client: %w", err)
	}
	bucket, err := ossClient.Bucket(bucketName)
	if err != nil {
		return nil, fmt.Errorf("115: open oss bucket: %w", err)
	}
	return bucket, nil
}

// abortUploadSession 幂等回收 multipart；已 complete/已 abort 返回 404
// （NoSuchUpload）视为成功。
func (d *Driver) abortUploadSession(ctx context.Context, bucket *oss.Bucket, tok p115Token) error {
	imur := oss.InitiateMultipartUploadResult{Bucket: tok.Bucket, Key: tok.Object, UploadID: tok.UploadID}
	if err := bucket.AbortMultipartUpload(imur); err != nil && !invalidResumedUploadSession(err) {
		return err
	}
	return nil
}

// listCompletedParts 用 OSS ListParts 从服务端重建已上传分片（本地零分片
// 状态）。分页拉到全部。查询失败返回错误，由调用方决定按临时故障全量重传。
func (d *Driver) listCompletedParts(ctx context.Context, bucket *oss.Bucket, object, uploadID, securityToken string) ([]oss.UploadPart, error) {
	var parts []oss.UploadPart
	marker := "0"
	for {
		resp, err := bucket.Client.Conn.DoWithContext(ctx, http.MethodGet, bucket.BucketName, object,
			map[string]any{"uploadId": uploadID, "max-parts": "1000", "part-number-marker": marker},
			map[string]string{driver115.OssSecurityTokenHeaderName: securityToken},
			nil, 0, nil)
		if err != nil {
			return nil, fmt.Errorf("115: list parts: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("115: list parts close response: %w", closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("115: list parts: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if readErr != nil {
			return nil, fmt.Errorf("115: list parts read response: %w", readErr)
		}
		var result oss.ListUploadedPartsResult
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("115: list parts response: %w", err)
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
