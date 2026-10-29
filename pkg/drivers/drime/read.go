package drime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.IsDir {
		return nil, fmt.Errorf("drime: cannot read directory %q: %w", entry.Name, drive.ErrInvalidInput)
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("drime: invalid read range offset=%d size=%d: %w", offset, size, drive.ErrInvalidInput)
	}
	if entry.Size == 0 || (entry.Size > 0 && offset >= entry.Size) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	target, err := d.contentURL(ctx, entry)
	if err != nil {
		d.setLastError(err)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+d.cl.token)
	end := ""
	if size > 0 {
		endOffset := offset + size - 1
		if entry.Size > 0 && endOffset >= entry.Size {
			endOffset = entry.Size - 1
		}
		end = strconv.FormatInt(endOffset, 10)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%s", offset, end))
	resp, err := d.cl.http.Do(req)
	if err != nil {
		d.cl.record(ctx, http.MethodGet, target, 0, 0, err)
		d.setLastError(err)
		return nil, fmt.Errorf("drime: read %q: %w", entry.Name, err)
	}
	d.cl.record(ctx, http.MethodGet, target, resp.StatusCode, 0, nil)
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		if entry.Size > 0 && offset >= entry.Size {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		return nil, fmt.Errorf("drime: read %q: range not satisfiable: %w", entry.Name, drive.ErrInvalidInput)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		err := drive.HTTPError("drime: read", req, resp, body)
		d.setLastError(err)
		return nil, err
	}
	if resp.StatusCode == http.StatusPartialContent {
		return d.limitRead(ctx, resp.Body), nil
	}
	// The provider ignored Range and returned the whole object. Discard the
	// prefix lazily and cap the returned length.
	reader := io.Reader(resp.Body)
	if size > 0 {
		reader = io.LimitReader(reader, size)
	}
	return d.limitRead(ctx, &skipReadCloser{body: resp.Body, reader: reader, skip: offset}), nil
}

func (d *Driver) limitRead(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	if d.limiter != nil {
		return d.limiter.LimitDownload(ctx, body)
	}
	return body
}

func (d *Driver) contentURL(ctx context.Context, entry drive.Entry) (string, error) {
	if raw := entryURL(entry); raw != "" {
		if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
			return raw, nil
		}
		return d.cl.downloadBase + strings.TrimPrefix(raw, "/"), nil
	}
	if entry.ID != "" {
		return d.cl.downloadBase + "api/v1/file-entries/" + url.PathEscape(entry.ID), nil
	}
	if hash := entryHash(entry); hash != "" {
		return d.cl.downloadBase + "api/v1/file-entries/download/" + url.PathEscape(hash), nil
	}
	var resp struct {
		FileEntry *apiEntry `json:"fileEntry"`
	}
	if _, err := d.cl.get(ctx, "/file-entries/"+url.PathEscape(entry.ID)+"/model?workspaceId="+url.QueryEscape(d.workspaceID), &resp); err != nil {
		return "", err
	}
	if resp.FileEntry == nil {
		return "", fmt.Errorf("drime: read %q: %w", entry.Name, drive.ErrNotFound)
	}
	mapped := resp.FileEntry.entry(entry.ParentID)
	if raw := entryURL(mapped); raw != "" {
		return d.cl.downloadBase + strings.TrimPrefix(raw, "/"), nil
	}
	if hash := entryHash(mapped); hash != "" {
		return d.cl.downloadBase + "api/v1/file-entries/download/" + url.PathEscape(hash), nil
	}
	return "", fmt.Errorf("drime: read %q: provider returned no content URL: %w", entry.Name, drive.ErrNotFound)
}

// skipReadCloser discards the first skip bytes on the first Read. It is used
// only when a provider ignores Range and returns the whole object.
type skipReadCloser struct {
	body   io.ReadCloser
	reader io.Reader
	skip   int64
	done   bool
}

func (r *skipReadCloser) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		if r.skip > 0 {
			if _, err := io.CopyN(io.Discard, r.body, r.skip); err != nil {
				if err == io.EOF {
					return 0, io.EOF
				}
				return 0, err
			}
		}
	}
	return r.reader.Read(p)
}

func (r *skipReadCloser) Close() error { return r.body.Close() }
