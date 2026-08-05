package quark

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"
	"github.com/yinzhenyu/qrypt/internal/retry"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	start := time.Now()
	for urlAttempt := 0; urlAttempt < 2; urlAttempt++ {
		if urlAttempt > 0 {
			d.invalidateURL(entry.ID)
		}
		downloadURL, urlCacheHit, err := d.downloadURL(ctx, entry.ID)
		if err != nil {
			logging.L.DebugfEvery("quark.read_url.error", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(start))
			return nil, err
		}
		logging.L.DebugfEvery("quark.read_url", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d refresh=%t dur=%s", entry.ID, offset, size, urlAttempt > 0, time.Since(start))

		resp, err := d.downloadRange(ctx, entry.ID, downloadURL, urlCacheHit, offset, size)
		if err != nil {
			logging.L.DebugfEvery("quark.read_http.error", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d err=%v", entry.ID, offset, size, err)
			return nil, err
		}
		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			if urlAttempt == 0 {
				logging.L.DebugfEvery("quark.read_http.forbidden", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d status=%d refresh_url=true", entry.ID, offset, size, resp.StatusCode)
				continue
			}
			return nil, fmt.Errorf("quark: read: download url forbidden")
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
		}
		body := d.limiter.LimitDownload(ctx, resp.Body)
		return &traceReadCloser{
			ReadCloser: body,
			fid:        entry.ID,
			offset:     offset,
			size:       size,
			start:      time.Now(),
		}, nil
	}
	return nil, fmt.Errorf("quark: read: download url refresh failed")
}

func (d *Driver) downloadRange(ctx context.Context, fid, downloadURL string, urlCacheHit bool, offset, size int64) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= quarkDownloadMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, fmt.Errorf("quark: read: create request: %w", err)
		}
		if size > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
		}
		var remoteAddr string
		var reused, wasIdle bool
		var idleTime time.Duration
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) {
				reused = info.Reused
				wasIdle = info.WasIdle
				idleTime = info.IdleTime
				if info.Conn != nil && info.Conn.RemoteAddr() != nil {
					remoteAddr = info.Conn.RemoteAddr().String()
				}
			},
		}))

		httpStart := time.Now()
		resp, err := d.cl.doDownload(req)
		event := drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(httpStart).String(),
			Attempt:   attempt + 1,
			Retry:     attempt > 0,
			RemoteID:  fid,
			Offset:    offset,
			Requested: size,
			Request: map[string]any{
				"range":         req.Header.Get("Range"),
				"url_host":      req.URL.Host,
				"url_cache_hit": urlCacheHit,
			},
			Response: map[string]any{
				"remote_addr":       remoteAddr,
				"reused_connection": reused,
				"was_idle":          wasIdle,
				"idle_time_ms":      quarkDurationMillis(idleTime),
			},
			Error: errorString(err),
		}
		if resp != nil {
			event.Response["content_length"] = resp.ContentLength
			event.Response["content_range"] = resp.Header.Get("Content-Range")
			event.Response["accept_ranges"] = resp.Header.Get("Accept-Ranges")
		}
		d.cl.recordMetric(ctx, event)
		if err != nil {
			lastErr = fmt.Errorf("quark: read: download: %w", err)
			logging.L.DebugfEvery("quark.read_http.error", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d attempt=%d err=%v dur=%s", fid, offset, size, attempt+1, err, time.Since(httpStart))
			if attempt < quarkDownloadMaxRetries && retryableHTTPError(err) {
				if waitErr := retry.WaitExponential(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, lastErr
		}
		logging.L.DebugfEvery("quark.read_http", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d attempt=%d status=%d dur=%s", fid, offset, size, attempt+1, resp.StatusCode, time.Since(httpStart))
		if retryableHTTPStatus(resp.StatusCode) && attempt < quarkDownloadMaxRetries {
			resp.Body.Close()
			if waitErr := retry.WaitExponential(ctx, attempt); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func (d *Driver) getURL(fid string) (string, bool) {
	if value, ok := d.urlCache.Load(fid); ok {
		cached := value.(cachedURL)
		if time.Now().Before(cached.expiry) {
			return cached.url, true
		}
	}
	return "", false
}

func (d *Driver) setURL(fid, url string) {
	d.urlCache.Store(fid, cachedURL{url: url, expiry: time.Now().Add(10 * time.Minute)})
}

func (d *Driver) invalidateURL(fid string) {
	d.urlCache.Delete(fid)
}

func (d *Driver) downloadURL(ctx context.Context, fid string) (string, bool, error) {
	if url, ok := d.getURL(fid); ok {
		d.recordDownloadURLMetric(ctx, fid, url, true, time.Now(), nil)
		return url, true, nil
	}
	started := time.Now()
	var resp downResp
	if err := d.cl.request(ctx, http.MethodPost, "/file/download", nil, map[string]any{
		"fids": []string{fid},
	}, &resp); err != nil {
		err = fmt.Errorf("quark: get download url: %w", err)
		d.recordDownloadURLMetric(ctx, fid, "", false, started, err)
		return "", false, err
	}
	if err := apiError(resp.respEnvelope); err != nil {
		d.recordDownloadURLMetric(ctx, fid, "", false, started, err)
		return "", false, err
	}
	if len(resp.Data) == 0 {
		err := fmt.Errorf("quark: no download url found")
		d.recordDownloadURLMetric(ctx, fid, "", false, started, err)
		return "", false, err
	}
	url := resp.Data[0].DownloadURL
	d.setURL(fid, url)
	d.recordDownloadURLMetric(ctx, fid, url, false, started, nil)
	return url, false, nil
}

func (d *Driver) recordDownloadURLMetric(ctx context.Context, fid, rawURL string, cacheHit bool, started time.Time, err error) {
	host := ""
	if rawURL != "" {
		if u, parseErr := url.Parse(rawURL); parseErr == nil {
			host = u.Host
		}
	}
	d.cl.recordMetric(ctx, drive.MetricEvent{
		Operation: "download_url",
		RemoteID:  fid,
		Duration:  time.Since(started).String(),
		Request: map[string]any{
			"url_cache_hit": cacheHit,
		},
		Response: map[string]any{
			"url_host": host,
		},
		Error: errorString(err),
	})
}
