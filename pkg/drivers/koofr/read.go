package koofr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// Read streams a byte range of a file from the provider's content endpoint.
// The entry id is the absolute provider path, so no per-read metadata request
// is issued.
func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.IsDir {
		return nil, fmt.Errorf("koofr: cannot read directory %q: %w", entry.Name, drive.ErrInvalidInput)
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("koofr: invalid read range offset=%d size=%d: %w", offset, size, drive.ErrInvalidInput)
	}
	path := d.resolveID(entry.ID)
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("koofr: read %q: invalid path: %w", entry.Name, drive.ErrInvalidInput)
	}
	if entry.Size == 0 || (entry.Size > 0 && offset >= entry.Size) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	query := url.Values{}
	query.Set("path", path)
	target := d.cl.content("/mounts/" + url.PathEscape(d.mountID) + "/files/get?" + query.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.SetBasicAuth(d.cl.email, d.cl.password)
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
		err = fmt.Errorf("koofr: read %q: %w", entry.Name, err)
		d.setLastError(err)
		return nil, err
	}
	d.cl.record(ctx, http.MethodGet, target, resp.StatusCode, 0, nil)
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = body
		if entry.Size > 0 && offset >= entry.Size {
			return io.NopCloser(bytes.NewReader(nil)), nil
		}
		err := fmt.Errorf("koofr: read %q: range not satisfiable: %w", entry.Name, drive.ErrInvalidInput)
		d.setLastError(err)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		apiResp := &apiResponse{req: req, resp: resp, body: body}
		err := apiResp.error("koofr: read")
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

func (d *Driver) limitRead(_ context.Context, body io.ReadCloser) io.ReadCloser {
	return body
}

// skipReadCloser discards the first skip bytes on the first Read. It is used
// only when the provider ignores Range and returns the whole object.
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
