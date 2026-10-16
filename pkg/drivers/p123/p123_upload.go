package p123

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

const (
	uploadBatchSize = 10
)

// s3SinglePutMax is the S3 PutObject ceiling; larger objects need multipart. It
// is a variable so tests can shrink the multipart window.
var s3SinglePutMax int64 = 5 * 1024 * 1024 * 1024

// uploadChunkSize matches the SliceSize 123pan reports for its S3 staging. It
// is a variable so tests can shrink the multipart window.
var uploadChunkSize int64 = 16 * 1024 * 1024

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashMD5}
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parent := d.resolve(req.ParentID)
	parentID, err := strconv.ParseInt(parent, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: upload: invalid parent id %q: %w", parent, drive.ErrInvalidInput)
	}
	name := req.Name
	size := req.Source.Size()

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	etag, err := sourceMD5Hex(ctx, req.Source)
	if err != nil {
		return drive.Entry{}, err
	}
	// Key the resume session by the *resolved* parent: aliases such as "0" or
	// "" stand for whatever the configured root is at the time, so keying on
	// the raw value could resume a stale handle after the root changes and
	// commit it into the wrong directory.
	sessionKey := session.Identity{
		ParentID:    parent,
		Name:        name,
		Size:        size,
		Fingerprint: etag,
	}.Key()

	drive.ReportUploadPhase(req.Progress, drive.UploadPhasePreparing)
	var (
		up        uploadRequestResp
		confirmed map[int]bool
	)
	for attempt := 0; ; attempt++ {
		up, confirmed, err = d.beginUpload(ctx, sessionKey, parentID, name, size, etag)
		if err != nil {
			break
		}
		if up.Data.Reuse || up.Data.Key == "" || hasStaticCredentials(up) {
			break
		}
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
		err = d.uploadPresigned(ctx, up, req, confirmed, sessionKey)
		if err == nil || attempt > 0 || !d.sessionStale(err) {
			break
		}
		// The provider dropped the stored upload handle; restart from a fresh
		// upload request instead of retrying a handle that no longer exists.
		logging.L.Warnf("[123] dropping stale upload session: %v", err)
		d.deleteSession(sessionKey)
	}
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("123: upload: %w", err)
	}

	var info *remoteFile
	switch {
	case up.Data.Reuse:
		// 123pan already stores this content; the completion call links it
		// into the target directory.
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
		info, err = d.cl.uploadCompleteV2(ctx, up, size, false)
	case hasStaticCredentials(up):
		// 123pan handed out static S3 credentials rather than presigned part
		// URLs; that upload is streamed directly and is not resumable.
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
		err = d.uploadStaticCredentials(ctx, up, req)
	case up.Data.Key == "":
		return drive.Entry{}, fmt.Errorf("123: upload: provider returned no upload key")
	default:
		drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
		info, err = d.cl.uploadCompleteV2(ctx, up, size, chunkCountFor(size) > 1)
	}
	if err != nil {
		d.setLastError(err)
		if drive.IsNonRetryable(err) {
			d.deleteSession(sessionKey)
		}
		return drive.Entry{}, fmt.Errorf("123: upload: %w", err)
	}
	// The provider commit succeeded, so the upload is done: the binding is
	// cleared before the (best-effort) entry lookup, and any leftover is
	// reclaimed by the expiry pass.
	if up.Data.Reuse {
		d.instantUploads.Add(1)
	}
	d.deleteSession(sessionKey)

	entry, err := d.finalizeUpload(ctx, parent, name, size, etag, info)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	return entry, nil
}

// uploadPresigned streams the source through the S3 presigned part URLs
// 123pan returns, skipping the parts a resumed session already confirmed.
// Single-part uploads use s3_upload_object/auth; multipart uploads fetch up to
// ten part URLs per batch.
func (d *Driver) uploadPresigned(ctx context.Context, up uploadRequestResp, req drive.UploadRequest, confirmed map[int]bool, sessionKey string) error {
	size := req.Source.Size()
	chunks := chunkCountFor(size)
	batchSize := 1
	getURLs := d.cl.s3Auth
	if chunks > 1 {
		batchSize = uploadBatchSize
		getURLs = d.cl.s3PreSignedURLs
	}

	file, err := req.Source.Open(ctx)
	if err != nil {
		return fmt.Errorf("123: upload: open source: %w", err)
	}
	defer file.Close()
	reader := d.limiter.LimitUpload(ctx, file)
	seeker, ok := reader.(io.ReadSeeker)
	if !ok {
		seeker = file
	}

	for start := 1; start <= chunks; start += batchSize {
		end := min(start+batchSize, chunks+1)
		if batchConfirmed(confirmed, start, end) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		urls, err := getURLs(ctx, up, start, end)
		if err != nil {
			return fmt.Errorf("%w: presign parts %d-%d: %w", errStaleUploadSession, start, end-1, err)
		}
		for part := start; part < end; part++ {
			if confirmed[part] {
				continue
			}
			rawURL := urls[strconv.Itoa(part)]
			if rawURL == "" {
				return fmt.Errorf("123: upload: missing presigned url for part %d", part)
			}
			offset := int64(part-1) * uploadChunkSize
			length := min(uploadChunkSize, size-offset)
			if err := d.cl.putPart(ctx, rawURL, seeker, offset, length); err != nil {
				return err
			}
			confirmed[part] = true
			d.markPartConfirmed(sessionKey, size, part)
			drive.ReportUploadProgress(req.Progress, length)
		}
	}
	return nil
}

func batchConfirmed(confirmed map[int]bool, start, end int) bool {
	for part := start; part < end; part++ {
		if !confirmed[part] {
			return false
		}
	}
	return true
}

func hasStaticCredentials(up uploadRequestResp) bool {
	return up.Data.AccessKeyID != "" && up.Data.SecretAccessKey != ""
}

// errStaleUploadSession marks a control-plane rejection of a persisted upload
// handle, distinguishing it from a transient transport failure.
var errStaleUploadSession = errors.New("123: stale upload session")

// sessionStale reports whether a presign failure means the stored provider
// upload is gone (restart) rather than a transient error (keep and retry).
func (d *Driver) sessionStale(err error) bool {
	if err == nil || !errors.Is(err, errStaleUploadSession) {
		return false
	}
	return !drive.RetryableCategory(drive.ErrorCategory(err))
}

// uploadStaticCredentials streams straight to the S3 endpoint 123pan returns
// when it hands out static credentials instead of presigned URLs, then confirms
// the upload with /file/upload_complete.
func (d *Driver) uploadStaticCredentials(ctx context.Context, up uploadRequestResp, req drive.UploadRequest) error {
	size := req.Source.Size()
	file, err := req.Source.Open(ctx)
	if err != nil {
		return fmt.Errorf("123: upload: open source: %w", err)
	}
	defer file.Close()

	cfg := aws.Config{
		Region:      "123pan",
		Credentials: credentials.NewStaticCredentialsProvider(up.Data.AccessKeyID, up.Data.SecretAccessKey, up.Data.SessionToken),
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(up.Data.EndPoint)
		o.UsePathStyle = true
	})
	reader := d.limiter.LimitUpload(ctx, file)
	bucket, key := up.Data.Bucket, up.Data.Key

	if size <= s3SinglePutMax {
		if _, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			Body:          reader,
			ContentLength: aws.Int64(size),
		}); err != nil {
			return fmt.Errorf("123: upload: s3 put: %w", err)
		}
	} else if err := d.s3MultipartUpload(ctx, client, bucket, key, reader, size, req.Progress); err != nil {
		return err
	}

	if err := d.cl.uploadComplete(ctx, up.Data.FileID); err != nil {
		return fmt.Errorf("123: upload: complete: %w", err)
	}
	return nil
}

func (d *Driver) s3MultipartUpload(ctx context.Context, client *s3.Client, bucket, key string, reader io.Reader, size int64, progress drive.UploadProgress) error {
	create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("123: upload: s3 create multipart: %w", err)
	}
	uploadID := aws.ToString(create.UploadId)
	abort := func() {
		_, _ = client.AbortMultipartUpload(context.WithoutCancel(ctx), &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(bucket),
			Key:      aws.String(key),
			UploadId: aws.String(uploadID),
		})
	}

	seeker, ok := reader.(io.ReadSeeker)
	if !ok {
		abort()
		return fmt.Errorf("123: upload: s3 multipart source is not seekable: %w", drive.ErrInvalidInput)
	}

	parts := make([]types.CompletedPart, 0, int((size+uploadChunkSize-1)/uploadChunkSize))
	for partNumber := 1; ; partNumber++ {
		offset := int64(partNumber-1) * uploadChunkSize
		if offset >= size {
			break
		}
		length := min(uploadChunkSize, size-offset)
		// The SDK must be able to rewind a part to retry a 5xx response, so the
		// body has to stay seekable: a bounded section reader over the source
		// keeps Seek, where io.LimitReader would drop it and turn any transient
		// failure into a hard "request stream is not seekable" error.
		body, err := newSectionReadSeeker(seeker, offset, length)
		if err != nil {
			abort()
			return fmt.Errorf("123: upload: s3 part %d: %w", partNumber, err)
		}
		out, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			UploadId:      aws.String(uploadID),
			PartNumber:    aws.Int32(int32(partNumber)),
			Body:          body,
			ContentLength: aws.Int64(length),
		})
		if err != nil {
			abort()
			return fmt.Errorf("123: upload: s3 part %d: %w", partNumber, err)
		}
		parts = append(parts, types.CompletedPart{
			ETag:       out.ETag,
			PartNumber: aws.Int32(int32(partNumber)),
		})
		drive.ReportUploadProgress(progress, length)
	}

	if _, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: parts,
		},
	}); err != nil {
		abort()
		return fmt.Errorf("123: upload: s3 complete multipart: %w", err)
	}
	return nil
}

// finalizeUpload turns the completion response into a drive.Entry. Fields the
// provider omitted (name, size, content hash) are filled on the remoteFile
// *before* the entry is built, so the public fields and the provider metadata
// on Entry.Extra can never disagree — a mismatch would make Read/Remove see a
// zero size and treat a non-empty upload as an empty file. It falls back to a
// listing when the provider returned no file_info (the static-credential path
// confirms with upload_complete instead of upload_complete/v2).
func (d *Driver) finalizeUpload(ctx context.Context, parent, name string, size int64, etag string, info *remoteFile) (drive.Entry, error) {
	if info != nil && info.FileID != 0 {
		merged := *info
		if merged.FileName == "" {
			merged.FileName = name
		}
		if merged.Size == 0 && size > 0 {
			merged.Size = size
		}
		if merged.Etag == "" {
			merged.Etag = strings.ToLower(etag)
		}
		if merged.ParentID == 0 {
			if id, err := strconv.ParseInt(parent, 10, 64); err == nil {
				merged.ParentID = id
			}
		}
		return merged.entry(parent), nil
	}
	entry, err := d.findChild(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("123: upload: locate committed file: %w", err)
	}
	return entry, nil
}

// sectionReadSeeker exposes the [base, base+size) window of an io.ReadSeeker as
// its own bounded, seekable stream. Offsets are relative to the window, so a
// Seek(0, io.SeekStart) rewinds to the start of the part — exactly what the AWS
// SDK does between retry attempts.
type sectionReadSeeker struct {
	src  io.ReadSeeker
	base int64
	size int64
	pos  int64
}

func newSectionReadSeeker(src io.ReadSeeker, offset, length int64) (*sectionReadSeeker, error) {
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("123: s3 section: seek source: %w", err)
	}
	return &sectionReadSeeker{src: src, base: offset, size: length}, nil
}

func (s *sectionReadSeeker) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}
	if remaining := s.size - s.pos; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := s.src.Read(p)
	s.pos += int64(n)
	return n, err
}

func (s *sectionReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.pos + offset
	case io.SeekEnd:
		target = s.size + offset
	default:
		return 0, fmt.Errorf("123: s3 section: invalid whence %d", whence)
	}
	if target < 0 {
		return 0, fmt.Errorf("123: s3 section: negative position %d", target)
	}
	if _, err := s.src.Seek(s.base+target, io.SeekStart); err != nil {
		return 0, fmt.Errorf("123: s3 section: seek source: %w", err)
	}
	s.pos = target
	return target, nil
}

func chunkCountFor(size int64) int {
	if size <= 0 {
		return 1
	}
	return int((size + uploadChunkSize - 1) / uploadChunkSize)
}

// sourceMD5Hex prefers a hash the caller already recorded on the source and
// scans it only when the MD5 needed for 123pan's dedup request is absent.
func sourceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashMD5); ok && len(sum) == md5.Size {
		return hex.EncodeToString(sum), nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("123: upload: hash source open: %w", err)
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("123: upload: hash source: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
