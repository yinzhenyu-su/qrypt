package p189

import (
	"context"
	"fmt"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("189: invalid offset or size")
	}
	fileID, err := strconv.ParseInt(entry.ID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("189: invalid file id: %w", err)
	}
	rawURL, err := d.cl.getDownloadURL(ctx, fileID)
	if err != nil {
		return nil, err
	}
	rawURL, err = d.resolveDownloadURL(ctx, normalizeDownloadURL(rawURL))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if size > 0 {
		end := offset + size - 1
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  "0s",
			Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		return nil, fmt.Errorf("189: read: %w", err)
	}
	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Status:    resp.StatusCode,
			Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
		})
		return d.limiter.LimitDownload(ctx, resp.Body), nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Request:   map[string]any{"offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": util.Snippet(raw)},
	})
	return nil, util.HTTPError("189: read", nil, resp, raw)
}

func (d *Driver) resolveDownloadURL(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{
		Jar: d.cl.hc.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		d.cl.recordMetric(ctx, drive.MetricEvent{
			Operation: "resolve_download_url",
			Method:    req.Method,
			URL:       traceURL(req.URL),
			Duration:  "0s",
			Error:     err.Error(),
		})
		return "", fmt.Errorf("189: resolve download url: %w", err)
	}
	defer resp.Body.Close()
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "resolve_download_url",
		Method:    req.Method,
		URL:       traceURL(req.URL),
		Status:    resp.StatusCode,
		Response:  map[string]any{"location": normalizeDownloadURL(resp.Header.Get("Location"))},
	})
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("189: resolve download url: redirect without location")
		}
		return normalizeDownloadURL(loc), nil
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return rawURL, nil
	}
	return "", fmt.Errorf("189: resolve download url: %s", resp.Status)
}

func normalizeDownloadURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "http://") {
		return "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	return rawURL
}

func (d *Driver) resolvePath(ctx context.Context, parentID int64, p string) (int64, error) {
	p = path.Clean(p)
	if p == "" || p == "." || p == "/" {
		return parentID, nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	currentID := parentID
	for _, part := range parts {
		folders, _, err := d.cl.listFiles(ctx, currentID)
		if err != nil {
			return 0, err
		}
		found := false
		for _, folder := range folders {
			if folder.Name == part {
				currentID = folder.ID
				found = true
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("%w: 189: path %q not found", drive.ErrNotFound, p)
		}
	}
	return currentID, nil
}
