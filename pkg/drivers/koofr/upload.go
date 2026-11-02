package koofr

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// RequiredUploadHashes returns none: the provider computes MD5 itself, so
// qrypt never needs to pre-hash an upload source for Koofr.
func (d *Driver) RequiredUploadHashes() []drive.HashAlgorithm {
	return nil
}

// PutSource uploads a file in one multipart request. The request always pins
// overwrite=true and autorename=false — the provider default silently forks
// colliding files under "name (1)" names — and always sends modified so an
// overwrite does not reset the mtime to server time.
//
// Koofr stores names case-insensitively: uploading B.bin over an existing
// b.bin overwrites b.bin while keeping its canonical name, which a
// case-sensitive caller reads as a new file silently clobbering a sibling.
// The driver therefore fold-checks the parent listing first and reports a
// conflict whenever a sibling differs from the requested name in casing
// only; an exact-name overwrite still replaces the entry deterministically.
func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	if err := validateName(req.Name); err != nil {
		return drive.Entry{}, err
	}
	parent, err := d.resolveTarget(req.ParentID, "upload")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: upload: %w", err)
	}
	if err := parent.checkWritable("upload", true); err != nil {
		return drive.Entry{}, err
	}
	if err := d.checkExtMountNameCollision(parent, "upload", req.Name); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	sibling, found, err := d.lookupChildOn(ctx, parent, req.Name)
	if err != nil {
		err = fmt.Errorf("koofr: upload %q: check sibling names: %w", req.Name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	if found && sibling.Name != req.Name {
		err := drive.NonRetryable(fmt.Errorf("koofr: upload %q: remote directory holds %q, a sibling differing only by case; refusing to overwrite it: %w", req.Name, sibling.Name, drive.ErrConflict))
		d.setLastError(err)
		return drive.Entry{}, err
	}
	file, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: open upload source: %w", err)
	}
	defer file.Close()

	query := url.Values{}
	query.Set("path", parent.path)
	query.Set("filename", req.Name)
	query.Set("info", "true")
	query.Set("overwrite", "true")
	query.Set("autorename", "false")
	if !req.ModTime.IsZero() {
		query.Set("modified", strconv.FormatInt(req.ModTime.UnixMilli(), 10))
	}
	target := d.cl.content("/mounts/" + url.PathEscape(parent.mount.ID) + "/files/put?" + query.Encode())

	// Build the multipart preamble once, then stream the source between the
	// preamble and the closing boundary. Buffering the whole source would
	// turn every upload into a heap-sized copy and report progress before a
	// single byte reaches the network.
	boundary := multipart.NewWriter(io.Discard).Boundary()
	var head bytes.Buffer
	writer := multipart.NewWriter(&head)
	if err := writer.SetBoundary(boundary); err != nil {
		return drive.Entry{}, err
	}
	if _, err := writer.CreateFormFile("file", req.Name); err != nil {
		return drive.Entry{}, err
	}
	tail := []byte("\r\n--" + boundary + "--\r\n")
	sourceSize := req.Source.Size()
	if sourceSize < 0 {
		return drive.Entry{}, fmt.Errorf("koofr: upload %q: invalid source size %d: %w", req.Name, sourceSize, drive.ErrInvalidInput)
	}
	contentLength := int64(head.Len()) + sourceSize + int64(len(tail))

	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseUploading)
	progressReader := drive.NewUploadProgressReader(req.Progress, file)
	body := io.MultiReader(bytes.NewReader(head.Bytes()), progressReader, bytes.NewReader(tail))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return drive.Entry{}, err
	}
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.ContentLength = contentLength
	httpReq.SetBasicAuth(d.cl.email, d.cl.password)
	start := time.Now()
	resp, err := d.cl.http.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		d.cl.record(ctx, http.MethodPost, target, 0, duration, err)
		err = fmt.Errorf("koofr: upload %q: %w", req.Name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	d.cl.record(ctx, http.MethodPost, target, resp.StatusCode, duration, nil)
	if readErr != nil {
		return drive.Entry{}, fmt.Errorf("koofr: upload %q: %w", req.Name, readErr)
	}
	apiResp := &apiResponse{req: httpReq, resp: resp, body: data}
	if !apiResp.ok() {
		err := apiResp.error("koofr: upload")
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity {
			err = drive.NonRetryable(err)
		}
		err = fmt.Errorf("koofr: upload %q: %w", req.Name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	var created apiFile
	if err := json.Unmarshal(data, &created); err != nil || created.Name == "" {
		return drive.Entry{}, fmt.Errorf("koofr: upload %q: decode response: %w", req.Name, drive.ErrInvalidInput)
	}
	// Live-observed on Google Drive external mounts: under a concurrent
	// upload of a case-variant name the server ignores autorename=false and
	// stores the object under a server-chosen name ("name (hex).bin"),
	// leaving an orphan the caller can never address and a permanent VFS
	// failure. The primary storage honors the pinned flags, so the guard
	// runs only for external mounts. A mismatched name deletes the misnamed
	// remote object and surfaces the race as a conflict.
	if parent.external && created.Name != req.Name {
		err := fmt.Errorf("koofr: upload %q: external mount %q stored the upload as %q despite autorename=false (concurrent upload conflict)", req.Name, parent.mount.Name, created.Name)
		if _, rmErr := d.cl.delete(ctx, "/mounts/"+url.PathEscape(parent.mount.ID)+"/files/remove", d.filesQuery(joinPath(parent.path, created.Name)), nil); rmErr != nil {
			err = fmt.Errorf("%s; deleting the misnamed remote object failed: %v", err, rmErr)
		}
		err = drive.NonRetryable(fmt.Errorf("%s: %w", err, drive.ErrConflict))
		d.setLastError(err)
		return drive.Entry{}, err
	}
	d.uploads.Add(1)
	drive.ReportUploadPhase(req.Progress, drive.UploadPhaseCompleted)
	return created.entry(d.idPath(parent, parent.path)), nil
}

// RemoteHash exposes the provider-computed MD5 for entries that carry one.
func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	hash := entryFileHash(entry)
	if !isMD5Hex(hash) {
		return "", "", drive.ErrUnsupported
	}
	return drive.HashMD5, hash, nil
}

// RemoteHashInfo labels the provider MD5 as provider-computed, because
// Koofr hashes the stored bytes server-side.
func (d *Driver) RemoteHashInfo(ctx context.Context, entry drive.Entry) (drive.RemoteHashInfo, error) {
	algorithm, hash, err := d.RemoteHash(ctx, entry)
	if err != nil {
		return drive.RemoteHashInfo{}, err
	}
	return drive.RemoteHashInfo{
		Algorithm: algorithm,
		Hash:      hash,
		Verified:  true,
		Source:    drive.RemoteHashSourceProvider,
	}, nil
}

func isMD5Hex(hash string) bool {
	if len(hash) != 32 {
		return false
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return false
	}
	return true
}
