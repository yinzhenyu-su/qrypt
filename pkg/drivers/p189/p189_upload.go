package p189

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID, name, source := req.ParentID, req.Name, req.Source
	parent, err := strconv.ParseInt(parentID, 10, 64)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("189: invalid parent id: %w", err)
	}
	size := source.Size()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseHashing)
	hashes, err := sourceUploadHashes(ctx, source, size, uploadPartSize)
	if err != nil {
		return drive.Entry{}, err
	}
	sessionKey := util.UploadSessionKey(parentID, name, size, hashes.FileMD5, hashes.SliceMD5)
	session, resumedSession := d.loadUploadSession(sessionKey)
	uploadFileID := session.UploadFileID
	fileDataExists := false
	if !resumedSession {
		uploadFileID, fileDataExists, err = d.cl.initUpload(ctx, parent, name, size, hashes.FileMD5, hashes.SliceMD5)
		if err != nil {
			return drive.Entry{}, err
		}
	}
	if !fileDataExists {
		if resumedSession {
			if session.CompletedParts == nil {
				session.CompletedParts = map[int]bool{}
			}
		} else {
			session = p189UploadSession{
				Key:            sessionKey,
				ParentID:       parentID,
				Name:           name,
				Size:           size,
				FileMD5:        hashes.FileMD5,
				SliceMD5:       hashes.SliceMD5,
				UploadFileID:   uploadFileID,
				PartSize:       uploadPartSize,
				CompletedParts: map[int]bool{},
			}
		}
		err := d.uploadParts(ctx, source, req.Progress, uploadFileID, hashes.Parts, session.CompletedParts, func(partNumber int) {
			session.CompletedParts[partNumber] = true
			d.saveUploadSession(session)
		})
		if err != nil {
			return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
		}
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if err := d.cl.commitUpload(ctx, uploadFileID, hashes.FileMD5, hashes.SliceMD5); err != nil {
		return drive.Entry{}, d.resumedUploadSessionError(resumedSession, sessionKey, err)
	}
	fileEntry, err := d.waitUploadedFile(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, err
	}
	d.deleteUploadSession(sessionKey)
	createdAt := parseTime(fileEntry.CreateDate)
	modTime := parseTime(fileEntry.LastOpTime)
	return drive.Entry{
		ID:        strconv.FormatInt(fileEntry.ID, 10),
		ParentID:  parentID,
		Name:      fileEntry.Name,
		Size:      fileEntry.Size,
		ModTime:   modTime,
		CreatedAt: createdAt,
		UpdatedAt: modTime,
	}, nil
}

func (d *Driver) uploadParts(ctx context.Context, source drive.ReadOnlyFileSource, progress drive.UploadProgress, uploadFileID string, parts []p189UploadPartMeta, completed map[int]bool, markComplete func(int)) error {
	file, err := source.Open(ctx)
	if err != nil {
		return fmt.Errorf("189: upload source open: %w", err)
	}
	defer file.Close()
	for _, meta := range parts {
		if completed[meta.Number] {
			drive.ReportUploadProgress(progress, meta.Size)
			continue
		}
		urls, err := d.cl.uploadData(ctx, uploadFileID, uploadPartInfo(meta))
		if err != nil {
			return fmt.Errorf("189: get upload url part %d: %w", meta.Number, err)
		}
		part := urls["partNumber_"+strconv.Itoa(meta.Number)]
		if part.RequestURL == "" {
			return drive.NonRetryable(fmt.Errorf("189: upload urls missing partNumber_%d", meta.Number))
		}
		offset := int64(meta.Number-1) * uploadPartSize
		uploadBody := drive.NewUploadProgressReader(progress, io.NewSectionReader(file, offset, meta.Size))
		uploadBody = d.limiter.LimitUpload(ctx, uploadBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.RequestURL, uploadBody)
		if err != nil {
			return err
		}
		req.ContentLength = meta.Size
		applyUploadHeaders(req, part.RequestHeader)
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		start := time.Now()
		resp, err := d.cl.hc.Do(req)
		if err != nil {
			d.cl.recordMetric(ctx, drive.MetricEvent{
				Operation: "upload_part",
				Method:    req.Method,
				URL:       traceURL(req.URL),
				Duration:  time.Since(start).String(),
				Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
				Error:     err.Error(),
			})
			return fmt.Errorf("189: upload part %d: %w", meta.Number, err)
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			d.cl.recordMetric(ctx, drive.MetricEvent{
				Operation: "upload_part",
				Method:    req.Method,
				URL:       traceURL(req.URL),
				Status:    resp.StatusCode,
				Duration:  time.Since(start).String(),
				Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
				Response:  map[string]any{"body_snippet": util.Snippet(raw)},
			})
			err := util.HTTPError(fmt.Sprintf("189: upload part %d", meta.Number), nil, resp, raw)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
				err = drive.NonRetryable(err)
			}
			return err
		}
		resp.Body.Close()
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "upload_part",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"part_number": meta.Number, "bytes": meta.Size, "headers": headerKeys(req.Header)},
		})
		if markComplete != nil {
			markComplete(meta.Number)
		}
	}
	return nil
}

func (d *Driver) waitUploadedFile(ctx context.Context, parentID int64, name string) (File, error) {
	var last []File
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		_, files, err := d.cl.listFiles(ctx, parentID)
		if err != nil {
			return File{}, err
		}
		last = files
		for _, file := range files {
			if file.Name == name {
				return file, nil
			}
		}
	}
	names := make([]string, len(last))
	for i, file := range last {
		names[i] = file.Name
	}
	return File{}, fmt.Errorf("189: uploaded file %q not visible after commit; files=%v", name, names)
}

func uploadPartInfo(part p189UploadPartMeta) string {
	return fmt.Sprintf("%d-%s", part.Number, part.MD5Base64)
}

func sourceUploadHashes(ctx context.Context, source drive.ReadOnlyFileSource, size, partSize int64) (p189UploadHashes, error) {
	if partSize <= 0 {
		return p189UploadHashes{}, fmt.Errorf("189: invalid upload part size")
	}
	if size <= partSize {
		fileMD5, err := sourceMD5Hex(ctx, source, size)
		if err != nil {
			return p189UploadHashes{}, err
		}
		part, err := uploadPartMeta(1, size, fileMD5)
		if err != nil {
			return p189UploadHashes{}, err
		}
		return p189UploadHashes{FileMD5: fileMD5, SliceMD5: fileMD5, Parts: []p189UploadPartMeta{part}}, nil
	}
	file, err := source.Open(ctx)
	if err != nil {
		return p189UploadHashes{}, fmt.Errorf("189: hash source open: %w", err)
	}
	defer file.Close()
	fileHash := md5.New()
	partCount := int((size + partSize - 1) / partSize)
	partHexes := make([]string, 0, partCount)
	parts := make([]p189UploadPartMeta, 0, partCount)
	buf := make([]byte, 1024*1024)
	for number := 1; number <= partCount; number++ {
		offset := int64(number-1) * partSize
		length := partSize
		if remaining := size - offset; remaining < length {
			length = remaining
		}
		partHash := md5.New()
		reader := io.NewSectionReader(file, offset, length)
		written, err := io.CopyBuffer(io.MultiWriter(fileHash, partHash), reader, buf)
		if err != nil {
			return p189UploadHashes{}, fmt.Errorf("189: hash source read part %d: %w", number, err)
		}
		if written != length {
			return p189UploadHashes{}, fmt.Errorf("189: hash source part %d size mismatch: read %d, expected %d", number, written, length)
		}
		partMD5 := partHash.Sum(nil)
		partHex := strings.ToUpper(hex.EncodeToString(partMD5))
		partHexes = append(partHexes, partHex)
		parts = append(parts, p189UploadPartMeta{
			Number:    number,
			Size:      length,
			MD5Hex:    partHex,
			MD5Base64: base64.StdEncoding.EncodeToString(partMD5),
		})
	}
	fileMD5 := strings.ToUpper(hex.EncodeToString(fileHash.Sum(nil)))
	sliceSum := md5.Sum([]byte(strings.Join(partHexes, "\n")))
	return p189UploadHashes{
		FileMD5:  fileMD5,
		SliceMD5: strings.ToUpper(hex.EncodeToString(sliceSum[:])),
		Parts:    parts,
	}, nil
}

func uploadPartMeta(number int, size int64, md5Hex string) (p189UploadPartMeta, error) {
	sum, err := hex.DecodeString(md5Hex)
	if err != nil {
		return p189UploadPartMeta{}, fmt.Errorf("189: decode part md5: %w", err)
	}
	return p189UploadPartMeta{
		Number:    number,
		Size:      size,
		MD5Hex:    strings.ToUpper(md5Hex),
		MD5Base64: base64.StdEncoding.EncodeToString(sum),
	}, nil
}

func uploadSliceMD5(fileMD5, sliceMD5 string, size int64) string {
	if size <= uploadPartSize {
		return fileMD5
	}
	return sliceMD5
}

func applyUploadHeaders(req *http.Request, raw string) {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		decoded = raw
	}
	for _, item := range strings.Split(decoded, "&") {
		if item == "" {
			continue
		}
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func headerKeys(headers http.Header) []string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func invalidResumedUploadSession(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "409") ||
		strings.Contains(s, "404") ||
		strings.Contains(s, "410") ||
		strings.Contains(s, "uploadFileId") ||
		strings.Contains(s, "InvalidUpload")
}

func sourceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
		if len(sum) != md5.Size {
			return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
		}
		return strings.ToUpper(hex.EncodeToString(sum)), nil
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: hash source open: %w", err)
	}
	defer body.Close()
	hash := md5.New()
	written, err := io.Copy(hash, body)
	if err != nil {
		return "", fmt.Errorf("189: hash source read: %w", err)
	}
	if written != size {
		return "", fmt.Errorf("189: hash source size mismatch: read %d, expected %d", written, size)
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

func sourceSliceMD5Hex(ctx context.Context, source drive.ReadOnlyFileSource, size int64) (string, error) {
	if size <= sliceMD5Size {
		if sum, ok := drive.SourceHash(source, drive.HashMD5); ok {
			if len(sum) != md5.Size {
				return "", fmt.Errorf("189: source MD5 metadata has %d bytes, want %d", len(sum), md5.Size)
			}
			return strings.ToUpper(hex.EncodeToString(sum)), nil
		}
	}
	sliceLen := size
	if sliceLen > sliceMD5Size {
		sliceLen = sliceMD5Size
	}
	body, err := source.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("189: slice hash source open: %w", err)
	}
	defer body.Close()
	buf := make([]byte, sliceLen)
	n, err := body.ReadAt(buf, 0)
	if err != nil && !(err == io.EOF && int64(n) == sliceLen) {
		return "", fmt.Errorf("189: slice hash source read: %w", err)
	}
	if int64(n) != sliceLen {
		return "", fmt.Errorf("189: slice hash source size mismatch: read %d, expected %d", n, sliceLen)
	}
	sum := md5.Sum(buf)
	return fmt.Sprintf("%X", sum), nil
}

func batchTaskInfos(infos ...batchTaskInfo) (string, error) {
	raw, err := json.Marshal(infos)
	if err != nil {
		return "", fmt.Errorf("189: encode batch task infos: %w", err)
	}
	return string(raw), nil
}
