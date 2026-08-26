// Package s3 implements an S3-compatible backend driver for qrypt.
//
// It communicates with AWS S3 and any S3-compatible object storage service
// (MinIO, Cloudflare R2, Backblaze B2, etc.) using the AWS SDK for Go v2.
//
// S3 has a flat namespace; directories are emulated via key prefixes and a
// delimiter during list operations. Placeholder files (default ".qrypt") mark
// empty directories so they survive after all children are deleted.
package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	stdpath "path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httputil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// Driver implements drive.Driver (plus Writer, SourceUploader, Debugger, and
// optional qrypt driver interfaces for S3-compatible object storage.
//
// Entry IDs are S3 key paths:
//   - Root: "/"
//   - Directory: "path/to/dir/"  (trailing slash)
//   - File:      "path/to/file.txt"
//
// ParentID is the directory prefix. List(parentID) returns the immediate
// children by querying the prefix with delimiter "/".
type Driver struct {
	drive.UnsupportedOperations
	bucket      string
	endpoint    string
	region      string
	customHost  string
	forcePath   bool
	listVersion string
	placeholder string
	rootPrefix  string

	accessKey    string
	secretKey    string
	sessionToken string

	signExpire time.Duration

	client  *s3.Client
	limiter *drive.BandwidthLimiter
	metrics *driverutil.Buffer

	sessions       *session.Index
	sessionStoreMu sync.Mutex
	sessionCancel  context.CancelFunc
}

// Options configures a new S3 driver.
type Options struct {
	Bucket          string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	CustomHost      string
	ForcePathStyle  bool
	ListVersion     string
	Placeholder     string
	RootPath        string
	SignURLExpire   time.Duration
}

const (
	defaultSignExpire = 4 * time.Hour

	s3MultipartPartSize = 16 * 1024 * 1024
	s3MultipartMinSize  = s3MultipartPartSize

	// s3SessionFile 存放"内容键 → 上传引用"绑定（不含任何 part 进度；
	// 进度通过 ListParts 从服务端重建）。
	s3SessionFile        = "s3_upload_sessions.json"
	s3SessionMaxAge      = session.DefaultMaxAge
	s3SessionExpiryEvery = time.Hour
)

// s3Token 是绑定中保存的 provider 侧上传引用。
type s3Token struct {
	UploadID string `json:"upload_id"`
	Object   string `json:"object"`
	PartSize int64  `json:"part_size,omitempty"`
}

type s3UploadPart struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

type s3UploadPartRange struct {
	Number int32
	Offset int64
	Size   int64
}

func init() {
	drive.Register("s3", func(params drive.Params) (drive.Driver, error) {
		bucket := params["bucket"]
		if bucket == "" {
			return nil, fmt.Errorf("s3: missing bucket")
		}
		endpoint := params["endpoint"]
		if endpoint == "" {
			return nil, fmt.Errorf("s3: missing endpoint")
		}
		opts := Options{
			Bucket:          bucket,
			Endpoint:        endpoint,
			Region:          params["region"],
			AccessKeyID:     params["access_key_id"],
			SecretAccessKey: params["secret_access_key"],
			SessionToken:    params["session_token"],
			CustomHost:      params["custom_host"],
			ForcePathStyle:  params["force_path_style"] == "true",
			ListVersion:     params["list_object_version"],
			Placeholder:     params["placeholder"],
			RootPath:        params["root_path"],
			SignURLExpire:   defaultSignExpire,
		}
		if v := params["sign_url_expire"]; v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				opts.SignURLExpire = d
			}
		}
		if opts.ListVersion == "" {
			opts.ListVersion = "v1"
		}
		if opts.Placeholder == "" {
			opts.Placeholder = ".qrypt"
		}
		return New(opts), nil
	},
		drive.ParamDef{
			Name:        "bucket",
			Type:        "string",
			Required:    true,
			Description: "S3 bucket name",
			Example:     "my-bucket",
		},
		drive.ParamDef{
			Name:        "endpoint",
			Type:        "string",
			Required:    true,
			Description: "S3 endpoint URL (e.g. https://s3.amazonaws.com, https://minio.example.com)",
			Example:     "https://s3.us-east-1.amazonaws.com",
		},
		drive.ParamDef{
			Name:        "region",
			Type:        "string",
			Description: "AWS region (default: us-east-1)",
			Default:     "us-east-1",
			Example:     "us-east-1",
		},
		drive.ParamDef{
			Name:        "access_key_id",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "S3 access key ID",
			Example:     "AKIA...",
		},
		drive.ParamDef{
			Name:        "secret_access_key",
			Type:        "string",
			Required:    true,
			Secret:      true,
			Description: "S3 secret access key",
			Example:     "...",
		},
		drive.ParamDef{
			Name:        "session_token",
			Type:        "string",
			Secret:      true,
			Description: "S3 session token (for temporary credentials)",
		},
		drive.ParamDef{
			Name:        "custom_host",
			Type:        "string",
			Description: "Custom host for download URLs (e.g. CDN domain)",
			Example:     "cdn.example.com",
		},
		drive.ParamDef{
			Name:        "force_path_style",
			Type:        "bool",
			Description: "Force path-style addressing (required for MinIO and most non-AWS S3)",
			Default:     "false",
			Example:     "true",
		},
		drive.ParamDef{
			Name:        "list_object_version",
			Type:        "string",
			Description: "S3 list API version: v1 or v2",
			Default:     "v1",
			Example:     "v2",
		},
		drive.ParamDef{
			Name:        "placeholder",
			Type:        "string",
			Description: "Placeholder filename for empty directories",
			Default:     ".qrypt",
			Example:     ".qrypt",
		},
		drive.ParamDef{
			Name:        "root_path",
			Type:        "string",
			Description: "Root path prefix within the bucket",
			Default:     "/",
			Example:     "/my-mount",
		},
		drive.ParamDef{
			Name:        "sign_url_expire",
			Type:        "duration",
			Description: "Presigned URL expiration duration",
			Default:     "4h",
			Example:     "1h",
		},
	)
}

// New creates a new S3 driver.
func New(opts Options) *Driver {
	rp := strings.Trim(opts.RootPath, "/")
	return &Driver{
		bucket:       opts.Bucket,
		endpoint:     opts.Endpoint,
		region:       opts.Region,
		customHost:   opts.CustomHost,
		forcePath:    opts.ForcePathStyle,
		listVersion:  opts.ListVersion,
		placeholder:  opts.Placeholder,
		rootPrefix:   rp,
		accessKey:    opts.AccessKeyID,
		secretKey:    opts.SecretAccessKey,
		sessionToken: opts.SessionToken,
		signExpire:   opts.SignURLExpire,
		metrics:      driverutil.NewBuffer(500),
	}
}

// ─── drive.Driver interface ────────────────────────────────────────────────

func (d *Driver) Init(ctx context.Context) error {
	cfg := d.awsConfig()
	d.client = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(d.endpoint)
		o.UsePathStyle = d.forcePath
	})
	start := time.Now()
	_, err := d.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(d.bucket),
	})
	d.recordSDK(ctx, "HeadBucket", start, map[string]any{"bucket": d.bucket}, err)
	if err != nil {
		return fmt.Errorf("s3: head bucket %q: %w", d.bucket, err)
	}
	return nil
}

func (d *Driver) Drop(ctx context.Context) error {
	d.sessionStoreMu.Lock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessionStoreMu.Unlock()
	if d.sessions != nil {
		_ = d.sessions.Flush()
	}
	return nil
}

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

// InstallStateStore 接入会话绑定索引并启动过期回收：绑定只保存
// "内容键 → UploadID"，part 进度在恢复时用 ListParts 重建。
func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.sessionStoreMu.Lock()
	defer d.sessionStoreMu.Unlock()
	if d.sessionCancel != nil {
		d.sessionCancel()
		d.sessionCancel = nil
	}
	d.sessions = session.NewIndex(store, s3SessionFile, session.IndexOptions{
		OnError: func(err error) {
			logging.L.Warnf("[S3] upload session state failed err=%v", err)
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	d.sessionCancel = cancel
	d.expireUploadSessions()
	go session.RunExpirer(ctx, s3SessionExpiryEvery, d.expireUploadSessions)
}

// List returns the immediate children of the directory identified by parentID.
// parentID is a key prefix like "/" (root) or "photos/".
func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	switch d.listVersion {
	case "v2":
		return d.listV2(ctx, parentID)
	default:
		return d.listV1(ctx, parentID)
	}
}

// Read downloads the object from S3 and returns an io.ReadCloser.
// offset/size map to the HTTP Range header.
func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.IsDir {
		return nil, fmt.Errorf("s3: cannot read directory %q", entry.ID)
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("s3: invalid range offset=%d size=%d", offset, size)
	}

	key := d.toS3Key(entry.ID)
	input := &s3.GetObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	}

	if offset > 0 || size > 0 {
		rangeEnd := ""
		if size > 0 {
			rangeEnd = fmt.Sprintf("%d", offset+size-1)
		}
		input.Range = aws.String(fmt.Sprintf("bytes=%d-%s", offset, rangeEnd))
	}

	start := time.Now()
	output, err := d.client.GetObject(ctx, input)
	d.recordSDK(ctx, "GetObject", start, map[string]any{"bucket": d.bucket, "key": key, "range": aws.ToString(input.Range)}, err)
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("%w: s3: not found %q", drive.ErrNotFound, entry.ID)
		}
		return nil, fmt.Errorf("s3: get %q: %w", entry.ID, err)
	}
	rc := output.Body
	if d.limiter != nil {
		rc = d.limiter.LimitDownload(ctx, rc)
	}
	return rc, nil
}

// ─── Driver write operations ────────────────────────────────────────────────

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	dirKey := d.toS3Key(d.joinPath(parentID, name)) + "/"
	emptyBody := strings.NewReader("")
	start := time.Now()
	_, err := d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(dirKey),
		Body:   emptyBody,
	})
	d.recordSDK(ctx, "PutObject", start, map[string]any{"bucket": d.bucket, "key": dirKey, "kind": "mkdir"}, err)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("s3: mkdir %q: %w", dirKey, err)
	}
	entryID := d.joinPath(parentID, name) + "/"
	now := time.Now()
	return drive.Entry{
		ID:        entryID,
		ParentID:  d.normParent(parentID),
		Name:      name,
		IsDir:     true,
		ModTime:   now,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	name := stdpath.Base(strings.TrimRight(entry.ID, "/"))
	return d.moveCopy(ctx, entry, dstParentID, name)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	parentID := stdpath.Dir(strings.TrimRight(entry.ID, "/"))
	return d.moveCopy(ctx, entry, parentID, newName)
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if entry.IsDir {
		return d.removeDir(ctx, entry.ID)
	}
	key := d.toS3Key(entry.ID)
	start := time.Now()
	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": key}, err)
	if err != nil {
		return fmt.Errorf("s3: remove %q: %w", entry.ID, err)
	}
	return nil
}

// ─── Driver source upload operation ─────────────────────────────────────────

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("s3: put source open: %w", err)
	}
	defer body.Close()

	key := d.toS3Key(d.joinPath(parentID, name))
	if source.Size() >= s3MultipartMinSize {
		sha256Hex, err := session.ContentSHA256Hex(ctx, source, source.Size())
		if err != nil {
			return drive.Entry{}, fmt.Errorf("s3: hash source %q: %w", name, err)
		}
		if err := d.putMultipartSource(ctx, parentID, name, key, source.Size(), sha256Hex, body, req.Progress); err != nil {
			return drive.Entry{}, err
		}
		now := time.Now()
		return drive.Entry{
			ID:        d.joinPath(parentID, name),
			ParentID:  d.normParent(parentID),
			Name:      name,
			Size:      source.Size(),
			ModTime:   now,
			CreatedAt: now,
			UpdatedAt: now,
		}, nil
	}

	var uploadBody = drive.NewUploadProgressReader(req.Progress, body)
	if d.limiter != nil {
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
	}
	start := time.Now()
	_, err = d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
		Body:   uploadBody,
	})
	d.recordSDK(ctx, "PutObject", start, map[string]any{"bucket": d.bucket, "key": key, "bytes": source.Size()}, err)
	if err != nil {
		err = fmt.Errorf("s3: put %q: %w", key, err)
		if nonRetryableUploadError(err) {
			err = drive.NonRetryable(err)
		}
		return drive.Entry{}, err
	}
	now := time.Now()
	return drive.Entry{
		ID:        d.joinPath(parentID, name),
		ParentID:  d.normParent(parentID),
		Name:      name,
		Size:      source.Size(),
		ModTime:   now,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func nonRetryableUploadError(err error) bool {
	var responseErr interface{ HTTPStatusCode() int }
	if errors.As(err, &responseErr) {
		status := responseErr.HTTPStatusCode()
		return httputil.IsNonRetryableClientStatus(status)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchBucket", "NoSuchKey", "InvalidBucketName", "InvalidObjectState", "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId", "EntityTooLarge":
			return true
		}
	}
	return false
}

// ─── drive.Driver observability ─────────────────────────────────────────────

func (d *Driver) DebugSnapshot(ctx context.Context) (drive.DebugSnapshot, error) {
	return drive.DebugSnapshot{
		Driver:      "s3",
		Health:      "ok",
		GeneratedAt: time.Now(),
		Stats: map[string]any{
			"bucket":                d.bucket,
			"endpoint":              d.endpoint,
			"region":                d.region,
			drive.DebugStatRootPath: d.rootPrefix,
			"list_version":          d.listVersion,
		},
		Extra: map[string]any{
			drive.DebugExtraCredentialSource: "config",
		},
	}, nil
}

func (d *Driver) metricEvents(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	return d.metrics.Events(since), nil
}

func (d *Driver) Space(ctx context.Context) (drive.Space, error) {
	return drive.Space{}, drive.ErrSpaceUnsupported
}

func (d *Driver) ResolvePath(ctx context.Context, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return "0", nil
	}
	return strings.Trim(strings.TrimPrefix(p, "/"), "/"), nil
}

func (d *Driver) Metrics(ctx context.Context, since time.Time) ([]drive.MetricEvent, error) {
	metrics, err := d.metricEvents(ctx, since)
	if err != nil {
		return nil, err
	}
	return drive.NormalizeMetricEvents("s3", metrics), nil
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityPathResolver,
		drive.CapabilityResumableUploader,
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilityRemoteNameResolver,
		drive.CapabilityServerSideCopy,
	}
}

// putMultipartSource 上传大文件。恢复只发生在内容指纹一致时（会话键内容寻址），
// 已传 part 通过 ListParts 从服务端重建；本地绑定只保存 UploadID，任何 part
// 进度都不落盘。provider 侧 commit 成功即成功，绑定清理不阻塞返回。
func (d *Driver) putMultipartSource(ctx context.Context, parentID, name, key string, size int64, sha256Hex string, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	sessionKey := session.Identity{ParentID: parentID, Name: name, Size: size, Fingerprint: sha256Hex}.Key()

	uploadID, completedParts, err := d.beginMultipartUpload(ctx, sessionKey, key, size)
	if err != nil {
		return err
	}

	ranges := s3UploadPartRanges(size, s3MultipartPartSize)
	// 恢复的已传 part 必须保留在最终提交列表里，不能只收集本次新传的。
	parts := append([]s3UploadPart(nil), completedParts...)
	completedByNumber := s3PartsByNumber(completedParts)
	for _, part := range ranges {
		if _, ok := completedByNumber[part.Number]; ok {
			drive.ReportUploadProgress(progress, part.Size)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		var uploadBody = drive.NewUploadProgressReader(progress, reader)
		if d.limiter != nil {
			uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		}
		start := time.Now()
		resp, err := d.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(d.bucket),
			Key:           aws.String(key),
			UploadId:      aws.String(uploadID),
			PartNumber:    aws.Int32(part.Number),
			Body:          uploadBody,
			ContentLength: aws.Int64(part.Size),
		})
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		d.recordSDK(ctx, "UploadPart", start, map[string]any{"bucket": d.bucket, "key": key, "part": part.Number, "bytes": part.Size}, err)
		if err != nil {
			err = fmt.Errorf("s3: upload part %d: %w", part.Number, err)
			if nonRetryableUploadError(err) {
				err = drive.NonRetryable(err)
			}
			if invalidResumedUploadSession(err) {
				d.sessions.Delete(sessionKey)
				return fmt.Errorf("s3: resumed upload session invalid, will retry from scratch: %w", err)
			}
			return err
		}
		parts = append(parts, s3UploadPart{Number: part.Number, ETag: aws.ToString(resp.ETag)})
		completedByNumber[part.Number] = s3UploadPart{Number: part.Number, ETag: aws.ToString(resp.ETag)}
		if d.sessions != nil {
			d.sessions.Touch(sessionKey)
		}
	}

	start := time.Now()
	_, err = d.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(d.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: s3CompletedParts(parts),
		},
	})
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	d.recordSDK(ctx, "CompleteMultipartUpload", start, map[string]any{"bucket": d.bucket, "key": key, "parts": len(parts)}, err)
	if err != nil {
		err = fmt.Errorf("s3: complete multipart upload %q: %w", key, err)
		if invalidResumedUploadSession(err) {
			d.sessions.Delete(sessionKey)
			return fmt.Errorf("s3: resumed upload session invalid, will retry from scratch: %w", err)
		}
		return err
	}
	if d.sessions != nil {
		d.sessions.Delete(sessionKey)
	}
	return nil
}

// beginMultipartUpload 返回复用的或新建的 multipart UploadID，以及从服务端
// 重建的已完成 part（全新上传时为空）。
//
// 全新上传先预留绑定、再创建 provider 上传：若绑定落盘后进程崩溃，下次尝试
// 会发现空 UploadID 绑定而作废重来，不会产生孤儿 multipart。
func (d *Driver) beginMultipartUpload(ctx context.Context, sessionKey, key string, size int64) (string, []s3UploadPart, error) {
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(sessionKey); ok {
			var tok s3Token
			if err := json.Unmarshal(binding.Token, &tok); err == nil && tok.UploadID != "" && tok.Object == key {
				parts, err := d.listCompletedParts(ctx, tok.UploadID, key)
				if err == nil {
					return tok.UploadID, parts, nil
				}
				if invalidResumedUploadSession(err) {
					d.sessions.Delete(sessionKey)
				} else {
					return "", nil, err
				}
			} else {
				// 无效或空 UploadID 绑定（预留后未完成创建）→ 作废重来。
				d.sessions.Delete(sessionKey)
			}
		}
	}

	// 预留绑定：内容寻址键落盘后任何崩溃都可恢复或可回收。
	token := s3Token{Object: key, PartSize: s3MultipartPartSize}
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return "", nil, fmt.Errorf("s3: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			return "", nil, fmt.Errorf("s3: persist upload session: %w", err)
		}
	}

	start := time.Now()
	resp, err := d.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(key),
	})
	d.recordSDK(ctx, "CreateMultipartUpload", start, map[string]any{"bucket": d.bucket, "key": key, "bytes": size}, err)
	if err != nil {
		if d.sessions != nil {
			d.sessions.Delete(sessionKey)
		}
		err = fmt.Errorf("s3: create multipart upload %q: %w", key, err)
		if nonRetryableUploadError(err) {
			err = drive.NonRetryable(err)
		}
		return "", nil, err
	}
	uploadID := aws.ToString(resp.UploadId)
	token.UploadID = uploadID
	if d.sessions != nil {
		if raw, err := json.Marshal(token); err != nil {
			return "", nil, fmt.Errorf("s3: encode upload session: %w", err)
		} else if err := d.sessions.Create(sessionKey, raw); err != nil {
			// 持 UploadID 落盘失败：立即回收刚创建的 provider 上传，避免孤儿。
			_ = d.abortMultipartUpload(context.Background(), key, uploadID)
			return "", nil, fmt.Errorf("s3: persist multipart upload id: %w", err)
		}
	}
	return uploadID, nil, nil
}

// listCompletedParts 从服务端重建已上传 part（本地不保存任何 part 状态）。
func (d *Driver) listCompletedParts(ctx context.Context, uploadID, key string) ([]s3UploadPart, error) {
	var parts []s3UploadPart
	var marker *string
	for {
		start := time.Now()
		out, err := d.client.ListParts(ctx, &s3.ListPartsInput{
			Bucket:           aws.String(d.bucket),
			Key:              aws.String(key),
			UploadId:         aws.String(uploadID),
			PartNumberMarker: marker,
		})
		d.recordSDK(ctx, "ListParts", start, map[string]any{"bucket": d.bucket, "key": key, "marker": marker != nil}, err)
		if err != nil {
			return nil, err
		}
		for _, p := range out.Parts {
			parts = append(parts, s3UploadPart{Number: aws.ToInt32(p.PartNumber), ETag: aws.ToString(p.ETag)})
		}
		if !aws.ToBool(out.IsTruncated) {
			return parts, nil
		}
		marker = out.NextPartNumberMarker
	}
}

// abortMultipartUpload 幂等回收一个 multipart 上传；已 complete/已 abort 的
// 会话返回 NoSuchUpload，视为成功。绑定删除后在过期回收时调用。
func (d *Driver) abortMultipartUpload(ctx context.Context, key, uploadID string) error {
	start := time.Now()
	_, err := d.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(d.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	d.recordSDK(ctx, "AbortMultipartUpload", start, map[string]any{"bucket": d.bucket, "key": key}, err)
	if err != nil && !invalidResumedUploadSession(err) {
		return err
	}
	return nil
}

// expireUploadSessions 回收超过 maxAge 未活动的绑定对应的 provider 上传。
func (d *Driver) expireUploadSessions() {
	if d.sessions == nil || d.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d.sessions.Expire(s3SessionMaxAge, time.Now(), func(binding session.Session) error {
		return d.reclaimUploadSession(ctx, binding)
	})
}

// reclaimUploadSession 幂等回收一个过期绑定；无效或空 token 没有可回收资源。
func (d *Driver) reclaimUploadSession(ctx context.Context, binding session.Session) error {
	var tok s3Token
	if err := json.Unmarshal(binding.Token, &tok); err != nil {
		return nil
	}
	if tok.UploadID == "" || tok.Object == "" {
		return nil
	}
	return d.abortMultipartUpload(ctx, tok.Object, tok.UploadID)
}

// ─── Internal ───────────────────────────────────────────────────────────────

func (d *Driver) toS3Key(id string) string {
	if id == "" || id == "0" || id == "/" || id == "root" {
		return d.rootPrefix
	}
	rel := strings.Trim(strings.TrimPrefix(id, "/"), "/")
	rel = strings.TrimPrefix(rel, "0/")
	if d.rootPrefix == "" {
		return rel
	}
	return d.rootPrefix + "/" + rel
}

func (d *Driver) relPath(s3Key string) string {
	if d.rootPrefix == "" {
		return s3Key
	}
	if s3Key == d.rootPrefix || s3Key == d.rootPrefix+"/" {
		return ""
	}
	return strings.TrimPrefix(s3Key, d.rootPrefix+"/")
}

func (d *Driver) normParent(parentID string) string {
	if parentID == "" || parentID == "0" || parentID == "/" || parentID == "root" {
		return "0"
	}
	return parentID
}

func (d *Driver) joinPath(parentID, name string) string {
	if d.normParent(parentID) == "0" {
		return name
	}
	return stdpath.Join(parentID, name)
}

// Copy implements drive.ServerSideCopier: S3 CopyObject — the provider
// copies the object without a data round trip through qrypt. Directory
// copies are rejected per the contract.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	srcKey := d.toS3Key(src.ID)
	dstKey := d.toS3Key(d.joinPath(dstParentID, dstName))
	copySource := url.PathEscape(d.bucket + "/" + srcKey)
	start := time.Now()
	_, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(d.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	})
	d.recordSDK(ctx, "CopyObject", start, map[string]any{"bucket": d.bucket, "src_key": srcKey, "dst_key": dstKey}, err)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("s3: copy %q → %q: %w", src.ID, dstKey, err)
	}
	return drive.Entry{
		ID:       d.joinPath(dstParentID, dstName),
		ParentID: d.normParent(dstParentID),
		Name:     dstName,
	}, nil
}

func (d *Driver) moveCopy(ctx context.Context, entry drive.Entry, dstParentID, newName string) error {
	dstKey := d.toS3Key(d.joinPath(dstParentID, newName))
	if entry.IsDir {
		return d.copyDir(ctx, entry.ID, dstKey+"/")
	}
	srcKey := d.toS3Key(entry.ID)
	copySource := url.PathEscape(d.bucket + "/" + srcKey)
	start := time.Now()
	_, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(d.bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	})
	d.recordSDK(ctx, "CopyObject", start, map[string]any{"bucket": d.bucket, "src_key": srcKey, "dst_key": dstKey}, err)
	if err != nil {
		return fmt.Errorf("s3: copy %q → %q: %w", entry.ID, dstKey, err)
	}
	start = time.Now()
	_, err = d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(srcKey),
	})
	d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": srcKey, "after": "copy"}, err)
	if err != nil {
		return fmt.Errorf("s3: delete source after copy %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) copyDir(ctx context.Context, srcID, dstPrefix string) error {
	entries, err := d.List(ctx, srcID)
	if err != nil {
		return fmt.Errorf("s3: copyDir list %q: %w", srcID, err)
	}
	for _, entry := range entries {
		srcChild := stdpath.Join(srcID, entry.Name)
		dstChild := stdpath.Join(dstPrefix, entry.Name)
		if entry.IsDir {
			if err := d.copyDir(ctx, srcChild, dstChild+"/"); err != nil {
				return err
			}
		} else {
			srcKey := d.toS3Key(srcChild)
			dstKey := d.toS3Key(dstChild)
			copySource := url.PathEscape(d.bucket + "/" + srcKey)
			start := time.Now()
			if _, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket:     aws.String(d.bucket),
				CopySource: aws.String(copySource),
				Key:        aws.String(dstKey),
			}); err != nil {
				d.recordSDK(ctx, "CopyObject", start, map[string]any{"bucket": d.bucket, "src_key": srcKey, "dst_key": dstKey}, err)
				return fmt.Errorf("s3: copyDir copy %q → %q: %w", srcChild, dstChild, err)
			} else {
				d.recordSDK(ctx, "CopyObject", start, map[string]any{"bucket": d.bucket, "src_key": srcKey, "dst_key": dstKey}, nil)
			}
		}
	}
	return nil
}

func (d *Driver) removeDir(ctx context.Context, dirID string) error {
	entries, err := d.List(ctx, dirID)
	if err != nil {
		return fmt.Errorf("s3: removeDir list %q: %w", dirID, err)
	}
	for _, entry := range entries {
		if entry.IsDir {
			childID := stdpath.Join(dirID, entry.Name)
			if err := d.removeDir(ctx, childID); err != nil {
				return err
			}
		} else {
			key := d.toS3Key(stdpath.Join(dirID, entry.Name))
			start := time.Now()
			if _, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(d.bucket),
				Key:    aws.String(key),
			}); err != nil && !isS3NotFound(err) {
				d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": key}, err)
				return fmt.Errorf("s3: removeDir delete %q: %w", key, err)
			} else {
				d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": key}, nil)
			}
		}
	}
	placeholderKey := d.toS3Key(stdpath.Join(dirID, d.placeholder))
	start := time.Now()
	_, err = d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(d.bucket),
		Key:    aws.String(placeholderKey),
	})
	d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": placeholderKey, "placeholder": true}, err)
	dirKey := d.toS3Key(dirID)
	if dirKey != "" {
		start = time.Now()
		_, err = d.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(d.bucket),
			Key:    aws.String(dirKey + "/"),
		})
		d.recordSDK(ctx, "DeleteObject", start, map[string]any{"bucket": d.bucket, "key": dirKey + "/", "dir_marker": true}, err)
	}
	return nil
}

func (d *Driver) recordSDK(ctx context.Context, operation string, start time.Time, request map[string]any, err error) {
	event := drive.MetricEvent{
		Layer:     "driver.sdk",
		Operation: operation,
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.metrics.Record(ctx, event)
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(apiErr.ErrorCode()) {
		case "nosuchupload", "invaliduploadid", "invalidrequest", "nosuchbucket":
			return true
		}
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nosuchupload") ||
		strings.Contains(s, "invalidupload") ||
		strings.Contains(s, "uploadid") ||
		strings.Contains(s, "upload id") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "409")
}

func s3UploadPartRanges(size, partSize int64) []s3UploadPartRange {
	if size <= 0 || partSize <= 0 {
		return nil
	}
	parts := make([]s3UploadPartRange, 0, int((size+partSize-1)/partSize))
	for offset, number := int64(0), int32(1); offset < size; offset, number = offset+partSize, number+1 {
		partBytes := partSize
		if remaining := size - offset; remaining < partBytes {
			partBytes = remaining
		}
		parts = append(parts, s3UploadPartRange{Number: number, Offset: offset, Size: partBytes})
	}
	return parts
}

func s3PartsByNumber(parts []s3UploadPart) map[int32]s3UploadPart {
	out := make(map[int32]s3UploadPart, len(parts))
	for _, part := range parts {
		if part.Number > 0 && part.ETag != "" {
			out[part.Number] = part
		}
	}
	return out
}

func s3CompletedParts(parts []s3UploadPart) []types.CompletedPart {
	sorted := append([]s3UploadPart(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Number < sorted[j].Number
	})
	out := make([]types.CompletedPart, 0, len(sorted))
	for _, part := range sorted {
		if part.Number <= 0 || part.ETag == "" {
			continue
		}
		out = append(out, types.CompletedPart{
			ETag:       aws.String(part.ETag),
			PartNumber: aws.Int32(part.Number),
		})
	}
	return out
}

// ─── S3 error helpers ───────────────────────────────────────────────────────

func isS3NotFound(err error) bool {
	var nfe *types.NoSuchKey
	return errors.As(err, &nfe)
}

// Compile-time interface checks.
var (
	_ drive.Driver              = (*Driver)(nil)
	_ drive.StateStoreInstaller = (*Driver)(nil)
)
