package quark

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
	"github.com/yinzhenyu/qrypt/pkg/util"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

var errDownloadURLExpired = errors.New("quark: download url expired")

const downloadWarmupMinSize = 512 * 1024

const (
	// Quark's CDN can keep a single Range stream at a comparatively low
	// throughput. Split the bounded VFS windows into a few independent Range
	// requests, as OpenList does for a full download. Keep the part size aligned
	// with qrypt's larger sequential read windows to reduce request overhead.
	downloadParallelMinSize  = 4 * 1024 * 1024
	downloadParallelPartSize = 8 * 1024 * 1024
	downloadParallelWorkers  = 3
)

const downloadChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	start := time.Now()
	var staleURL string
	for urlAttempt := 0; urlAttempt < 2; urlAttempt++ {
		if urlAttempt > 0 {
			d.invalidateURL(entry.ID, staleURL)
		}
		download, err := d.downloadURL(ctx, entry.ID)
		if err != nil {
			logging.L.DebugfEvery("quark.read_url.error", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d err=%v dur=%s", entry.ID, offset, size, err, time.Since(start))
			return nil, err
		}
		staleURL = download.url
		logging.L.DebugfEvery("quark.read_url", time.Second, "[QUARK] ReadURL fid=%q offset=%d size=%d refresh=%t cache_hit=%t dur=%s", entry.ID, offset, size, urlAttempt > 0, download.cacheHit, time.Since(start))
		if urlAttempt == 0 && size >= downloadWarmupMinSize {
			if err := d.warmDownloadConnection(ctx, entry.ID, download.url, download.cookie); errors.Is(err, errDownloadURLExpired) {
				continue
			}
		}

		body, err := d.downloadBody(ctx, entry.ID, download.url, download.cookie, download.cacheHit, offset, size)
		if errors.Is(err, errDownloadURLExpired) {
			if urlAttempt == 0 {
				logging.L.DebugfEvery("quark.read_http.forbidden", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d refresh_url=true", entry.ID, offset, size)
				continue
			}
			return nil, fmt.Errorf("quark: read: download url forbidden")
		}
		if err != nil {
			logging.L.DebugfEvery("quark.read_http.error", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d err=%v", entry.ID, offset, size, err)
			return nil, err
		}
		body = d.limiter.LimitDownload(ctx, body)
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

func (d *Driver) downloadBody(ctx context.Context, fid, downloadURL, cookieSnapshot string, urlCacheHit bool, offset, size int64) (io.ReadCloser, error) {
	if size >= downloadParallelMinSize {
		return d.downloadParallel(ctx, fid, downloadURL, cookieSnapshot, urlCacheHit, offset, size)
	}
	resp, err := d.downloadRange(ctx, fid, downloadURL, cookieSnapshot, urlCacheHit, offset, size)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, errDownloadURLExpired
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

type downloadRangePart struct {
	offset int64
	size   int64
	data   []byte
}

func (d *Driver) downloadParallel(ctx context.Context, fid, downloadURL, cookieSnapshot string, urlCacheHit bool, offset, size int64) (io.ReadCloser, error) {
	partCount := int((size + downloadParallelPartSize - 1) / downloadParallelPartSize)
	if partCount < 2 {
		resp, err := d.downloadRange(ctx, fid, downloadURL, cookieSnapshot, urlCacheHit, offset, size)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return nil, errDownloadURLExpired
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return nil, fmt.Errorf("quark: read: unexpected status %d", resp.StatusCode)
		}
		return resp.Body, nil
	}

	parts := make([]downloadRangePart, 0, partCount)
	for partOffset := offset; partOffset < offset+size; {
		partSize := min(int64(downloadParallelPartSize), offset+size-partOffset)
		parts = append(parts, downloadRangePart{offset: partOffset, size: partSize})
		partOffset += partSize
	}

	parallelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	workerCount := min(downloadParallelWorkers, len(parts))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-parallelCtx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					part := &parts[index]
					resp, err := d.downloadRange(parallelCtx, fid, downloadURL, cookieSnapshot, urlCacheHit, part.offset, part.size)
					if err == nil {
						if resp.StatusCode == http.StatusForbidden {
							resp.Body.Close()
							err = errDownloadURLExpired
						} else if resp.StatusCode != http.StatusPartialContent {
							resp.Body.Close()
							err = fmt.Errorf("quark: parallel read: unexpected status %d", resp.StatusCode)
						} else {
							part.data, err = readDownloadPart(resp, part.size)
						}
					}
					if err != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = err
							cancel()
						}
						errMu.Unlock()
					}
				}
			}
		}()
	}
sendLoop:
	for index := range parts {
		select {
		case jobs <- index:
		case <-parallelCtx.Done():
			break sendLoop
		}
		if parallelCtx.Err() != nil {
			break sendLoop
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		if errors.Is(firstErr, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, firstErr
	}

	var assembled bytes.Buffer
	assembled.Grow(int(size))
	for _, part := range parts {
		assembled.Write(part.data)
	}
	return io.NopCloser(bytes.NewReader(assembled.Bytes())), nil
}

func readDownloadPart(resp *http.Response, expected int64) ([]byte, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, expected+1))
	if err != nil {
		return nil, fmt.Errorf("quark: parallel read body: %w", err)
	}
	if int64(len(data)) != expected {
		return nil, fmt.Errorf("quark: parallel read body size mismatch: got=%d want=%d", len(data), expected)
	}
	return data, nil
}

// warmDownloadConnection establishes the CDN connection before the first
// user-visible Range request. Quark's first request to a signed download URL
// can include DNS/TCP/TLS setup and a slow cold CDN path; a one-byte Range
// keeps that cost out of the first seek. Warmup is best effort except for a
// 403, which means the URL must be refreshed before reading.
func (d *Driver) warmDownloadConnection(ctx context.Context, fid, downloadURL, cookieSnapshot string) error {
	if _, ok := d.downloadWarmupDone.Load(downloadURL); ok {
		return nil
	}
	_, err, _ := d.downloadWarmup.Do(downloadURL, func() (any, error) {
		if _, ok := d.downloadWarmupDone.Load(downloadURL); ok {
			return nil, nil
		}
		warmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(warmCtx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Range", "bytes=0-0")
		started := time.Now()
		resp, err := d.cl.doDownload(req, cookieSnapshot)
		event := drive.MetricEvent{
			Operation: "download_warmup",
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
			Status:    responseStatus(resp),
			Duration:  time.Since(started).String(),
			Attempt:   1,
			RemoteID:  fid,
			Requested: 1,
			Request: map[string]any{
				"range":    req.Header.Get("Range"),
				"url_host": req.URL.Host,
			},
			Response: map[string]any{},
			Error:    errorString(err),
		}
		if resp != nil {
			event.Response["content_length"] = resp.ContentLength
			event.Response["content_range"] = resp.Header.Get("Content-Range")
		}
		d.cl.recordMetric(ctx, event)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return nil, errDownloadURLExpired
		}
		if resp.StatusCode != http.StatusPartialContent {
			return nil, fmt.Errorf("quark: download warmup: unexpected status %d", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
		d.downloadWarmupDone.Store(downloadURL, struct{}{})
		return nil, nil
	})
	return err
}

func (d *Driver) downloadRange(ctx context.Context, fid, downloadURL, cookieSnapshot string, urlCacheHit bool, offset, size int64) (*http.Response, error) {
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
		resp, err := d.cl.doDownload(req, cookieSnapshot)
		event := drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
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
				if waitErr := util.WaitExponential(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, lastErr
		}
		logging.L.DebugfEvery("quark.read_http", time.Second, "[QUARK] ReadHTTP fid=%q offset=%d size=%d attempt=%d status=%d dur=%s", fid, offset, size, attempt+1, resp.StatusCode, time.Since(httpStart))
		if retryableHTTPStatus(resp.StatusCode) && attempt < quarkDownloadMaxRetries {
			resp.Body.Close()
			if waitErr := util.WaitExponential(ctx, attempt); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

func (d *Driver) getCachedURL(fid string) (cachedURL, bool) {
	if value, ok := d.urlCache.Load(fid); ok {
		cached := value.(cachedURL)
		if time.Now().Before(cached.expiry) {
			return cached, true
		}
	}
	return cachedURL{}, false
}

func (d *Driver) getURL(fid string) (string, bool) {
	cached, ok := d.getCachedURL(fid)
	return cached.url, ok
}

func (d *Driver) setURL(fid, url string) {
	d.setURLWithCookie(fid, url, d.cl.cookieValue())
}

func (d *Driver) setURLWithCookie(fid, url, cookieSnapshot string) {
	d.urlCache.Store(fid, cachedURL{url: url, cookie: cookieSnapshot, expiry: time.Now().Add(10 * time.Minute)})
}

func (d *Driver) invalidateURL(fid, staleURL string) {
	value, ok := d.urlCache.Load(fid)
	if !ok || value.(cachedURL).url != staleURL {
		return
	}
	d.urlCache.CompareAndDelete(fid, value)
	d.downloadWarmupDone.Delete(staleURL)
}

func (d *Driver) downloadURL(ctx context.Context, fid string) (downloadURLResult, error) {
	if cached, ok := d.getCachedURL(fid); ok {
		d.recordDownloadURLMetric(ctx, fid, cached.url, true, time.Now(), nil)
		return downloadURLResult{url: cached.url, cookie: cached.cookie, cacheHit: true}, nil
	}
	started := time.Now()
	value, err, _ := d.urlFetch.Do(fid, func() (any, error) {
		if cached, ok := d.getCachedURL(fid); ok {
			return downloadURLResult{url: cached.url, cookie: cached.cookie, cacheHit: true}, nil
		}
		var resp downResp
		cookieSnapshot := d.cl.cookieValue()
		if err := d.cl.requestWithCookie(ctx, http.MethodPost, "/file/download", nil, map[string]any{
			"fids": []string{fid},
		}, &resp, cookieSnapshot); err != nil {
			return nil, fmt.Errorf("quark: get download url: %w", err)
		}
		if err := apiError(resp.respEnvelope); err != nil {
			return nil, err
		}
		if len(resp.Data) == 0 {
			return nil, fmt.Errorf("quark: no download url found")
		}
		url := resp.Data[0].DownloadURL
		d.setURLWithCookie(fid, url, cookieSnapshot)
		return downloadURLResult{url: url, cookie: cookieSnapshot}, nil
	})
	if err != nil {
		d.recordDownloadURLMetric(ctx, fid, "", false, started, err)
		return downloadURLResult{}, err
	}
	result := value.(downloadURLResult)
	d.recordDownloadURLMetric(ctx, fid, result.url, result.cacheHit, started, nil)
	return result, nil
}

type downloadURLResult struct {
	url      string
	cookie   string
	cacheHit bool
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
