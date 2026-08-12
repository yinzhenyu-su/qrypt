package aliyundrive

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	rc, status, err := d.readWithDownloadURL(ctx, entry, offset, size, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		d.urlCache.Delete(entry.ID)
		if rc != nil {
			rc.Close()
		}
		rc, status, err = d.readWithDownloadURL(ctx, entry, offset, size, true)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK && status != http.StatusPartialContent {
		if rc != nil {
			rc.Close()
		}
		return nil, fmt.Errorf("aliyundrive: read status %d", status)
	}
	return d.limiter.LimitDownload(ctx, rc), nil
}

func (d *Driver) readWithDownloadURL(ctx context.Context, entry drive.Entry, offset, size int64, refresh bool) (io.ReadCloser, int, error) {
	url, err := d.downloadURL(ctx, entry.ID, refresh)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Referer", "https://www.alipan.com/")
	if size > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	}
	start := time.Now()
	httpResp, err := d.cl.httpClient.Do(req)
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       driverutil.URL(req.URL),
		Status:    responseStatus(httpResp),
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"range": req.Header.Get("Range")},
		Error:     errorString(err),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("aliyundrive: read: %w", err)
	}
	return httpResp.Body, httpResp.StatusCode, nil
}

func (d *Driver) downloadURL(ctx context.Context, fileID string, refresh bool) (string, error) {
	if !refresh {
		if cached, ok := d.urlCache.Load(fileID); ok {
			item := cachedDownloadURL{}
			if typed, ok := cached.(cachedDownloadURL); ok {
				item = typed
			}
			if item.URL != "" && time.Now().Before(item.ExpiresAt) {
				return item.URL, nil
			}
			d.urlCache.Delete(fileID)
		}
	}
	const expireSec = 14400
	var resp downloadURLResp
	body := map[string]any{
		"drive_id":   d.driveID,
		"file_id":    fileID,
		"expire_sec": expireSec,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/file/get_download_url", body, &resp); err != nil {
		return "", fmt.Errorf("aliyundrive: download url: %w", err)
	}
	if resp.URL == "" {
		return "", fmt.Errorf("aliyundrive: download url is empty")
	}
	d.urlCache.Store(fileID, cachedDownloadURL{
		URL:       resp.URL,
		ExpiresAt: time.Now().Add((expireSec - 300) * time.Second),
	})
	return resp.URL, nil
}
