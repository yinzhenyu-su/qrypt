// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) PutSource(ctx context.Context, req drive.UploadRequest) (drive.Entry, error) {
	parentID := d.resolveID(req.ParentID)
	body, err := req.Source.Open(ctx)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: put source open: %w", err)
	}
	defer body.Close()
	if req.Source.Size() <= oneDriveSmallUploadLimit {
		return d.putSmall(ctx, parentID, req.Name, req.Source.Size(), body, req.Progress)
	}
	return d.putLarge(ctx, parentID, req.Name, req.Source.Size(), body, req.Progress)
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

func (d *Driver) putLarge(ctx context.Context, parentID, name string, size int64, body drive.ReadOnlyFile, progress drive.UploadProgress) (drive.Entry, error) {
	var session createUploadSessionResp
	sessionPath := d.apiPath(fmt.Sprintf("/items/%s:/%s:/createUploadSession", url.PathEscape(parentID), escapePathSegment(name)))
	payload := map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"}}
	if err := d.requestJSON(ctx, http.MethodPost, sessionPath, payload, &session); err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q: %w", name, err)
	}
	if session.UploadURL == "" {
		return drive.Entry{}, fmt.Errorf("onedrive: create upload session %q returned empty uploadUrl", name)
	}
	for offset := int64(0); offset < size; offset += d.chunkSize {
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
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, uploadBody)
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
	}
	item, err := d.itemByChildName(ctx, parentID, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: resolve uploaded file %q: %w", name, err)
	}
	return item.entry(parentID), nil
}
