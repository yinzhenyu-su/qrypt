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
	"errors"
	"fmt"
	"io"
	"net/url"
	stdpath "path"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/yinzhenyu/qrypt/internal/driver/util"
	"github.com/yinzhenyu/qrypt/pkg/drive"
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

	client     *s3.Client
	limiter    *drive.BandwidthLimiter
	stateStore drive.StateStore
	metrics    *util.Buffer
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

	s3UploadSessionStateFile  = "s3_upload_sessions.json"
	s3UploadSessionMaxAge     = 24 * time.Hour
	s3UploadSessionMaxEntries = 1024
)

type s3UploadSession struct {
	Key      string         `json:"key"`
	Bucket   string         `json:"bucket"`
	Object   string         `json:"object"`
	UploadID string         `json:"upload_id"`
	ParentID string         `json:"parent_id"`
	Name     string         `json:"name"`
	Size     int64          `json:"size"`
	PartSize int64          `json:"part_size"`
	Parts    []s3UploadPart `json:"parts,omitempty"`
	SavedAt  time.Time      `json:"saved_at,omitempty"`
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
		metrics:      util.NewBuffer(500),
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

func (d *Driver) Drop(ctx context.Context) error { return nil }

func (d *Driver) InstallBandwidthLimiter(limiter *drive.BandwidthLimiter) drive.BandwidthLimitDirection {
	d.limiter = limiter
	return drive.BandwidthLimitDownload | drive.BandwidthLimitUpload
}

func (d *Driver) InstallStateStore(store drive.StateStore) {
	d.stateStore = store
	d.pruneStoredUploadSessions()
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
			return nil, fmt.Errorf("s3: not found %q", entry.ID)
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
	return drive.Entry{
		ID:       entryID,
		ParentID: d.normParent(parentID),
		Name:     name,
		IsDir:    true,
		ModTime:  time.Now(),
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
		if err := d.putMultipartSource(ctx, parentID, name, key, source.Size(), body, req.Progress); err != nil {
			return drive.Entry{}, err
		}
		return drive.Entry{
			ID:       d.joinPath(parentID, name),
			ParentID: d.normParent(parentID),
			Name:     name,
			Size:     source.Size(),
			ModTime:  time.Now(),
		}, nil
	}

	var uploadBody io.Reader = drive.NewUploadProgressReader(req.Progress, body)
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
	return drive.Entry{
		ID:       d.joinPath(parentID, name),
		ParentID: d.normParent(parentID),
		Name:     name,
		Size:     source.Size(),
		ModTime:  time.Now(),
	}, nil
}

func nonRetryableUploadError(err error) bool {
	var responseErr interface{ HTTPStatusCode() int }
	if errors.As(err, &responseErr) {
		status := responseErr.HTTPStatusCode()
		return status >= 400 && status < 500 && status != 408 && status != 429
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
	}
}

func (d *Driver) putMultipartSource(ctx context.Context, parentID, name, key string, size int64, body drive.ReadOnlyFile, progress drive.UploadProgress) error {
	sessionKey := util.UploadSessionKey(d.bucket, key, size)
	session, resumedSession := d.loadUploadSession(sessionKey)
	if !resumedSession {
		start := time.Now()
		resp, err := d.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: aws.String(d.bucket),
			Key:    aws.String(key),
		})
		d.recordSDK(ctx, "CreateMultipartUpload", start, map[string]any{"bucket": d.bucket, "key": key, "bytes": size}, err)
		if err != nil {
			err = fmt.Errorf("s3: create multipart upload %q: %w", key, err)
			if nonRetryableUploadError(err) {
				err = drive.NonRetryable(err)
			}
			return err
		}
		session = s3UploadSession{
			Key:      sessionKey,
			Bucket:   d.bucket,
			Object:   key,
			UploadID: aws.ToString(resp.UploadId),
			ParentID: parentID,
			Name:     name,
			Size:     size,
			PartSize: s3MultipartPartSize,
		}
	}

	ranges := s3UploadPartRanges(size, session.PartSize)
	completedByNumber := s3PartsByNumber(session.Parts)
	for _, part := range ranges {
		if completed, ok := completedByNumber[part.Number]; ok && completed.ETag != "" {
			drive.ReportUploadProgress(progress, part.Size)
			continue
		}
		if err := ctx.Err(); err != nil {
			return d.resumedUploadSessionError(resumedSession, sessionKey, err)
		}
		reader := io.NewSectionReader(body, part.Offset, part.Size)
		var uploadBody io.Reader = drive.NewUploadProgressReader(progress, reader)
		if d.limiter != nil {
			uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		}
		start := time.Now()
		resp, err := d.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(session.Bucket),
			Key:           aws.String(session.Object),
			UploadId:      aws.String(session.UploadID),
			PartNumber:    aws.Int32(part.Number),
			Body:          uploadBody,
			ContentLength: aws.Int64(part.Size),
		})
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		d.recordSDK(ctx, "UploadPart", start, map[string]any{"bucket": session.Bucket, "key": session.Object, "part": part.Number, "bytes": part.Size}, err)
		if err != nil {
			err = fmt.Errorf("s3: upload part %d: %w", part.Number, err)
			if nonRetryableUploadError(err) {
				err = drive.NonRetryable(err)
			}
			return d.resumedUploadSessionError(resumedSession, sessionKey, err)
		}
		session.Parts = upsertS3UploadPart(session.Parts, s3UploadPart{Number: part.Number, ETag: aws.ToString(resp.ETag)})
		completedByNumber[part.Number] = s3UploadPart{Number: part.Number, ETag: aws.ToString(resp.ETag)}
		d.saveUploadSession(session)
	}

	start := time.Now()
	_, err := d.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(session.Bucket),
		Key:      aws.String(session.Object),
		UploadId: aws.String(session.UploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: s3CompletedParts(session.Parts),
		},
	})
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	d.recordSDK(ctx, "CompleteMultipartUpload", start, map[string]any{"bucket": session.Bucket, "key": session.Object, "parts": len(session.Parts)}, err)
	if err != nil {
		err = fmt.Errorf("s3: complete multipart upload %q: %w", key, err)
		if nonRetryableUploadError(err) {
			err = drive.NonRetryable(err)
		}
		return d.resumedUploadSessionError(resumedSession, sessionKey, err)
	}
	d.deleteUploadSession(sessionKey)
	return nil
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

func (d *Driver) loadUploadSession(key string) (s3UploadSession, bool) {
	session, ok := d.uploadSessionStore().Load(key)
	return session, ok
}

func (d *Driver) saveUploadSession(session s3UploadSession) {
	d.uploadSessionStore().Save(session)
}

func (d *Driver) deleteUploadSession(key string) {
	d.uploadSessionStore().Delete(key)
}

func (d *Driver) pruneStoredUploadSessions() {
	d.uploadSessionStore().Prune()
}

func (d *Driver) uploadSessionStore() *util.UploadSessionStore[s3UploadSession] {
	return util.NewUploadSessionStore(util.UploadSessionStoreOptions[s3UploadSession]{
		Store:      d.stateStore,
		File:       s3UploadSessionStateFile,
		MaxAge:     s3UploadSessionMaxAge,
		MaxEntries: s3UploadSessionMaxEntries,
		Key: func(session s3UploadSession) string {
			return session.Key
		},
		Valid: func(key string, session s3UploadSession) bool {
			return session.Key != "" && session.Bucket != "" && session.Object != "" && session.UploadID != "" && session.PartSize > 0 && len(session.Parts) > 0
		},
		UpdatedAt: func(session s3UploadSession) time.Time {
			return session.SavedAt
		},
		Touch: func(session *s3UploadSession, now time.Time) {
			session.SavedAt = now
		},
	})
}

func (d *Driver) resumedUploadSessionError(resumed bool, key string, err error) error {
	if resumed && (drive.IsNonRetryable(err) || invalidResumedUploadSession(err)) {
		d.deleteUploadSession(key)
		return fmt.Errorf("s3: resumed upload session invalid, will retry from scratch: %v", err)
	}
	return err
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

func upsertS3UploadPart(parts []s3UploadPart, part s3UploadPart) []s3UploadPart {
	for i := range parts {
		if parts[i].Number == part.Number {
			parts[i] = part
			return parts
		}
	}
	return append(parts, part)
}

// ─── S3 error helpers ───────────────────────────────────────────────────────

func isS3NotFound(err error) bool {
	var nfe *types.NoSuchKey
	if errors.As(err, &nfe) {
		return true
	}
	return false
}

// Compile-time interface checks.
var (
	_ drive.Driver              = (*Driver)(nil)
	_ drive.StateStoreInstaller = (*Driver)(nil)
)
