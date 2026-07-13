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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/yinzhenyu/qrypt/internal/driver/traceutil"
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
	trace   *traceutil.Buffer
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

const defaultSignExpire = 4 * time.Hour

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
		trace:        traceutil.NewBuffer(500),
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

// ─── drive.Writer interface ─────────────────────────────────────────────────

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

// ─── drive.SourceUploader interface ─────────────────────────────────────────

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	body, err := source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("s3: put source open: %w", err)
	}
	defer body.Close()

	key := d.toS3Key(d.joinPath(parentID, name))
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
		return drive.Entry{}, fmt.Errorf("s3: put %q: %w", key, err)
	}
	return drive.Entry{
		ID:       d.joinPath(parentID, name),
		ParentID: d.normParent(parentID),
		Name:     name,
		Size:     source.Size(),
		ModTime:  time.Now(),
	}, nil
}

// ─── drive.Debugger interface ───────────────────────────────────────────────

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

func (d *Driver) DebugTrace(ctx context.Context, since time.Time) ([]drive.DebugTraceEvent, error) {
	return d.trace.Events(since), nil
}

func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}

func (d *Driver) Capabilities() []drive.Capability {
	return []drive.Capability{
		drive.CapabilityWriter,
		drive.CapabilitySourceUploader,
		drive.CapabilityDebugger,
		drive.CapabilityRemoteNameResolver,
	}
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
	event := drive.DebugTraceEvent{
		Layer:     "driver.sdk",
		Operation: operation,
		Duration:  time.Since(start).String(),
		Request:   request,
	}
	if err != nil {
		event.Error = err.Error()
	}
	d.trace.Record(ctx, event)
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
	_ drive.Driver             = (*Driver)(nil)
	_ drive.Writer             = (*Driver)(nil)
	_ drive.SourceUploader     = (*Driver)(nil)
	_ drive.Debugger           = (*Driver)(nil)
	_ drive.DebugTraceProvider = (*Driver)(nil)
	_ drive.RemoteNameResolver = (*Driver)(nil)
	_ drive.CapabilityReporter = (*Driver)(nil)
)
