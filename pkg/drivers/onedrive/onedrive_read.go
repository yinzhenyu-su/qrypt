// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if entry.IsDir {
		return nil, fmt.Errorf("onedrive: cannot read directory %q", entry.ID)
	}
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("onedrive: invalid range offset=%d size=%d", offset, size)
	}
	if entry.Size > 0 && offset >= entry.Size {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	item, err := d.itemByID(ctx, entry.ID)
	if err != nil {
		return nil, fmt.Errorf("onedrive: get download url %q: %w", entry.ID, err)
	}
	if item.DownloadURL == "" {
		return nil, fmt.Errorf("onedrive: item %q has no download url", entry.ID)
	}
	downloadURL := d.applyCustomHost(item.DownloadURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 || size > 0 {
		end := ""
		if size > 0 {
			endOffset := offset + size - 1
			if entry.Size > 0 && endOffset >= entry.Size {
				endOffset = entry.Size - 1
			}
			end = strconv.FormatInt(endOffset, 10)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%s", offset, end))
	}
	start := time.Now()
	resp, err := d.client.Do(req)
	d.recordHTTP(ctx, "download", http.MethodGet, downloadURL, start, respStatus(resp), err)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && entry.Size > 0 && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, util.HTTPError("onedrive: download", nil, resp, body)
	}
	rc := resp.Body
	if d.limiter != nil {
		rc = d.limiter.LimitDownload(ctx, rc)
	}
	return rc, nil
}
