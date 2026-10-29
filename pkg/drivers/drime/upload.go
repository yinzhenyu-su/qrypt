package drime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drive/session"
)

var errStaleUpload = errors.New("drime: stale upload handle")

type uploadSessionToken struct {
	Key      string `json:"key"`
	UploadID string `json:"upload_id"`
	PartSize int64  `json:"part_size"`
}

func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return []drive.HashAlgorithm{drive.HashSHA256}
}

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	remote, err := encodeRemoteName(req.Name)
	if err != nil {
		return drive.Entry{}, err
	}
	parent := d.resolveID(req.ParentID)
	size := req.Source.Size()
	drive.ReportUploadPhase(req.Progress, drive.UploadPhasePreparing)
	hash, err := session.ContentSHA256Hex(ctx, req.Source, size)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: hash upload source: %w", err)
	}
	if d.instantUpload {
		if entry, ok := d.tryInstantUpload(ctx, parent, req, hash); ok {
			return entry, nil
		}
	}
	if size < smallUploadLimit && d.presignSmall {
		entry, err := d.putPresigned(ctx, parent, req, remote, hash)
		if err != nil {
			d.setLastError(err)
			return drive.Entry{}, err
		}
		return entry, nil
	}
	if size < smallUploadLimit {
		entry, err := d.putSmall(ctx, parent, req, remote, hash)
		if err != nil {
			d.setLastError(err)
			return drive.Entry{}, err
		}
		return entry, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		entry, err := d.putLarge(ctx, parent, req, remote, size, hash)
		if err == nil {
			return entry, nil
		}
		if errors.Is(err, errStaleUpload) && attempt == 0 {
			continue
		}
		d.setLastError(err)
		return drive.Entry{}, err
	}
	return drive.Entry{}, fmt.Errorf("drime: upload %q: stale upload handle", req.Name)
}

func (d *Driver) tryInstantUpload(ctx context.Context, parent string, req drive.UploadRequest, hash string) (drive.Entry, bool) {
	// The hash is client-declared until verify-integrity runs. Verify the
	// candidate's stored bytes before reusing it so a stale or malicious hash
	// cannot make qrypt skip an upload of different content.
	var check apiHashCheckResponse
	resp, err := d.cl.post(ctx, "/file-entries/hash/check", map[string]any{
		"fileHash":    hash,
		"workspaceId": json.Number(d.workspaceID),
	}, nil)
	if err != nil {
		return drive.Entry{}, false
	}
	if err := json.Unmarshal(resp.body, &check); err != nil || !check.Exists || check.Entry == nil || check.Entry.ID == 0 {
		return drive.Entry{}, false
	}
	var verify apiVerifyResponse
	vr, err := d.cl.post(ctx, "/file-entries/"+url.PathEscape(strconv.FormatInt(check.Entry.ID, 10))+"/verify-integrity", map[string]any{"sha256": hash}, nil)
	if err != nil {
		return drive.Entry{}, false
	}
	if err := json.Unmarshal(vr.body, &verify); err != nil || !verify.Verified {
		return drive.Entry{}, false
	}
	var destination any
	if parent != "" {
		destination = json.Number(parent)
	}
	dup, err := d.cl.post(ctx, "/file-entries/duplicate?workspaceId="+url.QueryEscape(d.workspaceID), map[string]any{
		"entryIds":      []string{strconv.FormatInt(check.Entry.ID, 10)},
		"destinationId": destination,
	}, nil)
	if err != nil {
		return drive.Entry{}, false
	}
	var body apiMoveResponse
	if err := json.Unmarshal(dup.body, &body); err != nil || len(body.Entries) == 0 {
		return drive.Entry{}, false
	}
	entry := body.Entries[0].entry(parent)
	entry.ParentID = parent
	if entry.Name != req.Name {
		renamed, renameErr := d.renameEntry(ctx, entry, req.Name)
		if renameErr != nil {
			_ = d.Remove(ctx, entry)
			return drive.Entry{}, false
		}
		entry = renamed
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseInstant)
	drive.ReportUploadProgress(req.Progress, req.Source.Size())
	d.instantUploads.Add(1)
	return entry, true
}

func (d *Driver) putPresigned(ctx context.Context, parent string, req drive.UploadRequest, remote, hash string) (drive.Entry, error) {
	file, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: open upload source: %w", err)
	}
	defer file.Close()
	var data bytes.Buffer
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	var reader io.Reader = file
	if d.limiter != nil {
		reader = d.limiter.LimitUpload(ctx, reader)
	}
	if _, err := io.Copy(&data, drive.NewUploadProgressReader(req.Progress, reader)); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: read upload source: %w", err)
	}
	var parentValue any
	if parent != "" {
		parentValue = json.Number(parent)
	}
	resp, err := d.cl.post(ctx, "/s3/simple/presign", map[string]any{
		"filename":    remote,
		"mime":        "application/octet-stream",
		"size":        req.Source.Size(),
		"extension":   extensionFor(remote),
		"workspaceId": json.Number(d.workspaceID),
		"parentId":    parentValue,
	}, nil)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: presign upload: %w", err)
	}
	var pre apiPresignResponse
	if err := json.Unmarshal(resp.body, &pre); err != nil || pre.URL == "" || pre.Key == "" {
		return drive.Entry{}, fmt.Errorf("drime: presign upload: invalid response: %w", drive.ErrInvalidInput)
	}
	put, err := d.cl.doRaw(ctx, http.MethodPut, pre.URL, bytes.NewReader(data.Bytes()), int64(data.Len()), "application/octet-stream", false)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: presigned PUT: %w", err)
	}
	if !put.ok() {
		return drive.Entry{}, put.error("drime: presigned PUT")
	}
	entry, err := d.createEntry(ctx, parent, req, remote, req.Source.Size(), hash, uploadSessionToken{Key: pre.Key})
	if err != nil {
		return drive.Entry{}, err
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	return entry, nil
}

func (d *Driver) putSmall(ctx context.Context, parent string, req drive.UploadRequest, remote, hash string) (drive.Entry, error) {
	file, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: open upload source: %w", err)
	}
	defer file.Close()
	var data bytes.Buffer
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	var reader io.Reader = file
	if d.limiter != nil {
		reader = d.limiter.LimitUpload(ctx, reader)
	}
	if _, err := io.Copy(&data, drive.NewUploadProgressReader(req.Progress, reader)); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: read upload source: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("parentId", parent)
	_ = writer.WriteField("workspaceId", d.workspaceID)
	if !req.ModTime.IsZero() {
		_ = writer.WriteField("lastModified", strconv.FormatInt(req.ModTime.UnixMilli(), 10))
	}
	_ = writer.WriteField("fileHash", hash)
	part, err := writer.CreateFormFile("file", remote)
	if err != nil {
		return drive.Entry{}, err
	}
	if _, err := part.Write(data.Bytes()); err != nil {
		return drive.Entry{}, err
	}
	if err := writer.Close(); err != nil {
		return drive.Entry{}, err
	}
	resp, err := d.cl.doRaw(ctx, http.MethodPost, d.cl.endpoint("/uploads"), &body, int64(body.Len()), writer.FormDataContentType(), true)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: upload %q: %w", req.Name, err)
	}
	if !resp.ok() {
		err := resp.error("drime: upload")
		if resp.status() == http.StatusUnprocessableEntity {
			err = drive.NonRetryable(err)
		}
		return drive.Entry{}, fmt.Errorf("drime: upload %q: %w", req.Name, err)
	}
	var parsed apiUploadResponse
	if err := json.Unmarshal(resp.body, &parsed); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode upload response: %w", err)
	}
	if parsed.FileEntry.ID == 0 {
		return drive.Entry{}, fmt.Errorf("drime: upload %q: provider returned no entry: %w", req.Name, drive.ErrInvalidInput)
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	entry := parsed.FileEntry.entry(parent)
	if entry.Size == 0 && req.Source.Size() == 0 {
		entry.Size = 0
	}
	return entry, nil
}

func (d *Driver) putLarge(ctx context.Context, parent string, req drive.UploadRequest, remote string, size int64, hash string) (drive.Entry, error) {
	chunk := d.chunkSizeFor(size)
	key := session.Identity{ParentID: parent, Name: req.Name, Size: size, Fingerprint: hash}.Key()
	token, uploaded, err := d.beginUpload(ctx, key, parent, remote, size, chunk)
	if err != nil {
		return drive.Entry{}, err
	}
	file, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: open upload source: %w", err)
	}
	defer file.Close()

	parts := (size + chunk - 1) / chunk
	etags := make(map[int]string, parts)
	for k, v := range uploaded {
		etags[k] = v
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	pending := make([]int, 0, parts)
	for part := 1; part <= int(parts); part++ {
		if etags[part] == "" {
			pending = append(pending, part)
		}
	}
	if len(pending) > 0 {
		workerCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		sem := make(chan struct{}, d.uploadConcurrency)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var progressMu sync.Mutex
		var firstErr error
		setErr := func(err error) {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			cancel()
		}
		for _, part := range pending {
			wg.Add(1)
			go func(part int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-workerCtx.Done():
					return
				}
				defer func() { <-sem }()
				if workerCtx.Err() != nil {
					return
				}
				offset := int64(part-1) * chunk
				length := chunk
				if offset+length > size {
					length = size - offset
				}
				signed, err := d.signPart(workerCtx, token, part)
				if err != nil {
					if errors.Is(err, drive.ErrNotFound) {
						d.deleteSession(key)
						err = errStaleUpload
					}
					setErr(err)
					return
				}
				section := io.NewSectionReader(file, offset, length)
				var partReader io.Reader = section
				if d.limiter != nil {
					partReader = d.limiter.LimitUpload(workerCtx, partReader)
				}
				resp, err := d.cl.doRaw(workerCtx, http.MethodPut, signed, partReader, length, "application/octet-stream", false)
				if err != nil {
					setErr(fmt.Errorf("drime: upload part %d: %w", part, err))
					return
				}
				if !resp.ok() {
					err := resp.error(fmt.Sprintf("drime: upload part %d", part))
					if errors.Is(err, drive.ErrNotFound) {
						d.deleteSession(key)
						err = errStaleUpload
					}
					setErr(err)
					return
				}
				etag := resp.header("ETag")
				if etag == "" {
					setErr(fmt.Errorf("drime: upload part %d: missing ETag: %w", part, drive.ErrInvalidInput))
					return
				}
				mu.Lock()
				etags[part] = etag
				mu.Unlock()
				progressMu.Lock()
				drive.ReportUploadProgress(req.Progress, length)
				progressMu.Unlock()
				if d.sessions != nil {
					d.sessions.Touch(key)
				}
			}(part)
		}
		wg.Wait()
		cancel()
		mu.Lock()
		err := firstErr
		mu.Unlock()
		if err != nil {
			return drive.Entry{}, err
		}
	}
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCommitting)
	if err := d.completeUpload(ctx, token, etags, parts); err != nil {
		if errors.Is(err, drive.ErrNotFound) {
			d.deleteSession(key)
			return drive.Entry{}, errStaleUpload
		}
		return drive.Entry{}, err
	}
	entry, err := d.createEntry(ctx, parent, req, remote, size, hash, token)
	if err != nil {
		return drive.Entry{}, err
	}
	d.deleteSession(key)
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	return entry, nil
}

func (d *Driver) beginUpload(ctx context.Context, key, parent, name string, size, chunk int64) (uploadSessionToken, map[int]string, error) {
	if d.sessions != nil {
		if binding, ok := d.sessions.Get(key); ok {
			var token uploadSessionToken
			if err := json.Unmarshal(binding.Token, &token); err == nil && token.Key != "" && token.UploadID != "" && token.PartSize == chunk {
				d.sessions.Touch(key)
				uploaded, err := d.getUploadedParts(ctx, token)
				if err == nil {
					return token, uploaded, nil
				}
				if !errors.Is(err, drive.ErrNotFound) {
					return uploadSessionToken{}, nil, err
				}
				d.sessions.Delete(key)
			} else {
				d.sessions.Delete(key)
			}
		}
		if err := d.sessions.Create(key, nil); err != nil {
			return uploadSessionToken{}, nil, fmt.Errorf("drime: persist upload session: %w", err)
		}
	}
	var parentValue any
	if parent != "" {
		parentValue = json.Number(parent)
	}
	var created apiMultipartCreateResponse
	resp, err := d.cl.post(ctx, "/s3/multipart/create", map[string]any{
		"filename":    name,
		"mime":        "application/octet-stream",
		"size":        size,
		"extension":   extensionFor(name),
		"workspaceId": json.Number(d.workspaceID),
		"parentId":    parentValue,
	}, nil)
	if err != nil {
		d.deleteSession(key)
		return uploadSessionToken{}, nil, fmt.Errorf("drime: create multipart upload: %w", err)
	}
	if err := json.Unmarshal(resp.body, &created); err != nil {
		d.deleteSession(key)
		return uploadSessionToken{}, nil, fmt.Errorf("drime: decode multipart create: %w", err)
	}
	if created.Key == "" || created.UploadID == "" {
		d.deleteSession(key)
		return uploadSessionToken{}, nil, fmt.Errorf("drime: multipart create: provider returned no key/uploadId: %w", drive.ErrInvalidInput)
	}
	token := uploadSessionToken{Key: created.Key, UploadID: created.UploadID, PartSize: chunk}
	if d.sessions != nil {
		if err := d.sessions.Create(key, mustJSONRaw(token)); err != nil {
			return uploadSessionToken{}, nil, fmt.Errorf("drime: persist upload session: %w", err)
		}
	}
	return token, nil, nil
}

func (d *Driver) getUploadedParts(ctx context.Context, token uploadSessionToken) (map[int]string, error) {
	var resp apiUploadedPartsResponse
	r, err := d.cl.post(ctx, "/s3/multipart/get-uploaded-parts", map[string]any{
		"key":      token.Key,
		"uploadId": token.UploadID,
	}, nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(r.body, &resp); err != nil {
		return nil, fmt.Errorf("drime: decode uploaded parts: %w", err)
	}
	out := map[int]string{}
	for _, part := range resp.Parts {
		if part.PartNumber > 0 && part.ETag != "" {
			out[part.PartNumber] = part.ETag
		}
	}
	return out, nil
}

func (d *Driver) signPart(ctx context.Context, token uploadSessionToken, part int) (string, error) {
	var resp apiSignPartsResponse
	r, err := d.cl.post(ctx, "/s3/multipart/batch-sign-part-urls", map[string]any{
		"key":         token.Key,
		"uploadId":    token.UploadID,
		"partNumbers": []int{part},
	}, nil)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(r.body, &resp); err != nil {
		return "", fmt.Errorf("drime: decode signed part: %w", err)
	}
	if len(resp.URLs) != 1 || resp.URLs[0].URL == "" {
		return "", fmt.Errorf("drime: sign part %d: provider returned %d urls: %w", part, len(resp.URLs), drive.ErrInvalidInput)
	}
	return resp.URLs[0].URL, nil
}

func (d *Driver) completeUpload(ctx context.Context, token uploadSessionToken, etags map[int]string, parts int64) error {
	numbers := make([]int, 0, parts)
	for part := 1; part <= int(parts); part++ {
		numbers = append(numbers, part)
	}
	sort.Ints(numbers)
	payload := make([]map[string]any, 0, len(numbers))
	for _, part := range numbers {
		etag := etags[part]
		if etag == "" {
			return fmt.Errorf("drime: complete upload: missing ETag for part %d: %w", part, drive.ErrInvalidInput)
		}
		payload = append(payload, map[string]any{"PartNumber": part, "ETag": etag})
	}
	_, err := d.cl.post(ctx, "/s3/multipart/complete", map[string]any{
		"key":      token.Key,
		"uploadId": token.UploadID,
		"parts":    payload,
	}, nil)
	if err != nil {
		return fmt.Errorf("drime: complete multipart upload: %w", err)
	}
	return nil
}

func (d *Driver) createEntry(ctx context.Context, parent string, req drive.UploadRequest, remote string, size int64, hash string, token uploadSessionToken) (drive.Entry, error) {
	uuid := token.Key
	if i := strings.LastIndex(uuid, "/"); i >= 0 {
		uuid = uuid[i+1:]
	}
	var parentValue any
	if parent != "" {
		parentValue = json.Number(parent)
	}
	payload := map[string]any{
		"filename":        uuid,
		"size":            size,
		"clientName":      remote,
		"clientMime":      "application/octet-stream",
		"clientExtension": extensionFor(req.Name),
		"parentId":        parentValue,
		"workspaceId":     json.Number(d.workspaceID),
		"fileHash":        hash,
	}
	if !req.ModTime.IsZero() {
		payload["lastModified"] = req.ModTime.UnixMilli()
	}
	resp, err := d.cl.post(ctx, "/s3/entries", payload, nil)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: create entry after upload: %w", err)
	}
	var created apiUploadResponse
	if err := json.Unmarshal(resp.body, &created); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode upload entry: %w", err)
	}
	if created.FileEntry.ID == 0 {
		return drive.Entry{}, fmt.Errorf("drime: create entry after upload: provider returned no entry: %w", drive.ErrInvalidInput)
	}
	return created.FileEntry.entry(parent), nil
}

func extensionFor(name string) string {
	ext := strings.TrimPrefix(path.Ext(name), ".")
	if ext == "" {
		return "bin"
	}
	return ext
}

func (d *Driver) deleteSession(key string) {
	if d.sessions != nil {
		d.sessions.Delete(key)
	}
}

func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	if entry.IsDir {
		return "", "", drive.ErrUnsupported
	}
	hash := strings.ToLower(entryFileHash(entry))
	if len(hash) != 64 {
		return "", "", drive.ErrUnsupported
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", "", drive.ErrUnsupported
		}
	}
	return drive.HashSHA256, hash, nil
}
