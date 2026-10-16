package p123

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

var errDownloadURLExpired = errors.New("123: download url expired")

type cachedDownload struct {
	url string
	at  int64
}

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("123: read: offset and size must be non-negative: %w", drive.ErrInvalidInput)
	}
	meta, sizeKnown, err := d.refreshMeta(ctx, entry)
	if err != nil {
		return nil, err
	}
	// A provider-confirmed zero-length object has nothing to read, and probing
	// its range would only provoke a 416 from the CDN.
	if offset >= meta.Size && (meta.Size > 0 || sizeKnown) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	length := clampSize(size, meta.Size, offset)

	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			d.forgetDownloadURL(entry.ID)
		}
		target, err := d.resolveDownloadURL(ctx, entry.ID, meta)
		if err != nil {
			if errors.Is(err, errDownloadURLExpired) {
				continue
			}
			d.setLastError(err)
			return nil, err
		}
		body, err := d.fetchRange(ctx, target, offset, length)
		if errors.Is(err, errDownloadURLExpired) {
			continue
		}
		if err != nil {
			d.setLastError(err)
			return nil, err
		}
		return d.limiter.LimitDownload(ctx, body), nil
	}
	return nil, fmt.Errorf("123: read: download url refresh failed")
}

// clampSize fixes the requested window to the known remote size so the Range
// header never asks past EOF.
func clampSize(size, total, offset int64) int64 {
	if size <= 0 || total <= 0 {
		return size
	}
	if offset+size > total {
		remaining := total - offset
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	return size
}

// refreshMeta returns provider metadata from the entry, re-listing the parent
// when the entry did not carry it (for example a caller-constructed entry). The
// sizeKnown result reports whether Size came from provider metadata, so a zero
// value reliably means an empty object rather than "unknown".
func (d *Driver) refreshMeta(ctx context.Context, entry drive.Entry) (fileMeta, bool, error) {
	if meta, ok := metaFromEntry(entry); ok && meta.S3KeyFlag != "" && meta.Etag != "" {
		return meta, true, nil
	}
	parentID := entry.ParentID
	if parentID == "" {
		if meta, ok := metaFromEntry(entry); ok && meta.ParentID != 0 {
			parentID = strconv.FormatInt(meta.ParentID, 10)
		}
	}
	if parentID == "" {
		return fallbackMeta(entry), false, nil
	}
	entries, err := d.List(ctx, parentID)
	if err != nil {
		return fileMeta{}, false, err
	}
	for _, candidate := range entries {
		if candidate.ID == entry.ID {
			if meta, ok := metaFromEntry(candidate); ok {
				return meta, true, nil
			}
		}
	}
	return fileMeta{}, false, fmt.Errorf("123: read: %w: entry %q", drive.ErrNotFound, entry.Name)
}

// resolveDownloadURL returns a signed CDN URL for the file, reusing a cached
// one while it is still fresh. It first confirms the object exists, because
// download_info keeps handing out working URLs for recycled files.
func (d *Driver) resolveDownloadURL(ctx context.Context, fileID string, meta fileMeta) (string, error) {
	if cached, ok := d.getCachedDownloadURL(fileID); ok {
		return cached, nil
	}
	exists, err := d.cl.exists(ctx, meta.FileID)
	if err != nil {
		return "", fmt.Errorf("123: read: %w", err)
	}
	if !exists {
		d.forgetDownloadURL(fileID)
		return "", fmt.Errorf("123: read: %w: file %d", drive.ErrNotFound, meta.FileID)
	}
	raw, err := d.cl.downloadInfo(ctx, meta)
	if err != nil {
		return "", fmt.Errorf("123: read: %w", err)
	}
	target, err := d.resolveCDNURL(ctx, raw)
	if err != nil {
		return "", err
	}
	d.setCachedDownloadURL(fileID, target)
	return target, nil
}

// maxRedirectHops bounds how many redirects one download_info URL may follow.
// Some CDN nodes bounce through an interstitial before the content, and the
// download client deliberately does not follow redirects itself so the driver
// can keep its Range request and inspect each hop.
const maxRedirectHops = 5

// resolveCDNURL turns a download_info URL into the final content URL. The
// provider embeds the real URL as a base64 `params` query value, and each hop
// answers with either a Location header or a JSON `data.redirect_url`
// envelope, so the chain is followed hop by hop until a hop yields content.
func (d *Driver) resolveCDNURL(ctx context.Context, raw string) (string, error) {
	target := decodeParamsURL(raw)
	for hop := 0; hop < maxRedirectHops; hop++ {
		next, final, err := d.resolveCDNHop(ctx, target)
		if err != nil {
			return "", err
		}
		if final {
			return next, nil
		}
		target = next
	}
	return "", fmt.Errorf("123: read: too many redirects (limit %d)", maxRedirectHops)
}

// resolveCDNHop performs one resolve request, preserving the Range header, and
// reports whether target already serves content.
func (d *Driver) resolveCDNHop(ctx context.Context, target string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false, fmt.Errorf("123: read: build resolve request: %w", err)
	}
	req.Header.Set("Referer", refererHeader)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := d.cl.downloadClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("123: read: resolve download url: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		location := resp.Header.Get("Location")
		if location == "" {
			return "", false, fmt.Errorf("123: read: redirect without location (%d)", resp.StatusCode)
		}
		return resolveRedirectURL(target, location), false, nil
	case resp.StatusCode < 300:
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			return "", false, fmt.Errorf("123: read: resolve response: %w", readErr)
		}
		if redirect := parseRedirectURL(data); redirect != "" {
			return resolveRedirectURL(target, redirect), false, nil
		}
		return target, true, nil
	case resp.StatusCode == http.StatusForbidden:
		// The signed URL is expired or rejected; let the caller refresh it.
		return "", false, errDownloadURLExpired
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// The probe asks for bytes 0-0; a 416 means this URL is serving an
		// empty object, so it is already the content URL.
		return target, true, nil
	default:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", false, drive.HTTPError("123: read: resolve", req, resp, data)
	}
}

// decodeParamsURL unwraps the base64 `params` query value 123pan hides the real
// download URL behind. A raw URL without params is returned unchanged.
func decodeParamsURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	params := u.Query().Get("params")
	if params == "" {
		return raw
	}
	decoded, err := base64.StdEncoding.DecodeString(params)
	if err != nil || len(decoded) == 0 {
		return raw
	}
	return string(decoded)
}

// resolveRedirectURL makes a redirect target absolute against the URL that
// produced it: CDNs answer with anything from a scheme-relative "//host/path"
// to a bare "/path", neither of which is usable as a request URL on its own.
func resolveRedirectURL(base, location string) string {
	ref, err := url.Parse(location)
	if err != nil {
		return location
	}
	if ref.IsAbs() {
		return ref.String()
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return location
	}
	return baseURL.ResolveReference(ref).String()
}

func parseRedirectURL(data []byte) string {
	var out struct {
		Data struct {
			RedirectURL string `json:"redirect_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return ""
	}
	return out.Data.RedirectURL
}

func (d *Driver) fetchRange(ctx context.Context, target string, offset, size int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("123: read: build request: %w", err)
	}
	switch {
	case size > 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))
	case offset > 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	req.Header.Set("Referer", refererHeader)
	resp, err := d.cl.downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("123: read: download: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		return resp.Body, nil
	case http.StatusOK:
		// Some CDN nodes ignore a Range request and return the whole object.
		// Treating that as the requested window would hand the caller bytes
		// from the wrong offset, so skip the prefix and cap the length.
		if offset > 0 || size > 0 {
			logging.L.DebugfEvery("p123.read.range_ignored", time.Second,
				"[123] CDN ignored Range for %s: offset=%d size=%d", driverutil.URL(req.URL), offset, size)
		}
		return newOffsetSkipReadCloser(resp.Body, offset, size), nil
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	case http.StatusForbidden:
		resp.Body.Close()
		return nil, errDownloadURLExpired
	default:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, drive.HTTPError("123: read", req, resp, data)
	}
}

// offsetSkipReadCloser adapts a full-object 200 response into the requested
// window: it lazily discards the leading offset bytes (never buffering the
// skipped prefix) and then hands back at most limit bytes. limit < 0 means
// "to EOF".
type offsetSkipReadCloser struct {
	rc        io.ReadCloser
	remaining int64
	limit     int64
	scratch   []byte
}

func newOffsetSkipReadCloser(rc io.ReadCloser, offset, size int64) io.ReadCloser {
	limit := size
	if size <= 0 {
		limit = -1
	}
	if offset <= 0 && limit < 0 {
		return rc
	}
	return &offsetSkipReadCloser{rc: rc, remaining: offset, limit: limit}
}

func (r *offsetSkipReadCloser) Read(p []byte) (int, error) {
	for r.remaining > 0 {
		if r.scratch == nil {
			r.scratch = make([]byte, 32*1024)
		}
		chunk := int64(len(r.scratch))
		if chunk > r.remaining {
			chunk = r.remaining
		}
		n, err := r.rc.Read(r.scratch[:chunk])
		r.remaining -= int64(n)
		if err != nil {
			return 0, err
		}
	}
	if r.limit == 0 {
		return 0, io.EOF
	}
	if r.limit > 0 && int64(len(p)) > r.limit {
		p = p[:r.limit]
	}
	n, err := r.rc.Read(p)
	if r.limit > 0 {
		r.limit -= int64(n)
	}
	return n, err
}

func (r *offsetSkipReadCloser) Close() error {
	return r.rc.Close()
}

func (d *Driver) getCachedDownloadURL(fileID string) (string, bool) {
	d.urlMu.Lock()
	defer d.urlMu.Unlock()
	cached, ok := d.urlCache[fileID]
	if !ok {
		return "", false
	}
	if time.Now().Unix()-cached.at > int64(downloadCacheTTL.Seconds()) {
		delete(d.urlCache, fileID)
		return "", false
	}
	return cached.url, true
}

func (d *Driver) setCachedDownloadURL(fileID, target string) {
	d.urlMu.Lock()
	defer d.urlMu.Unlock()
	if len(d.urlCache) >= downloadURLCacheMax {
		for key := range d.urlCache {
			delete(d.urlCache, key)
			break
		}
	}
	d.urlCache[fileID] = cachedDownload{url: target, at: time.Now().Unix()}
}

func (d *Driver) forgetDownloadURL(fileID string) {
	d.urlMu.Lock()
	delete(d.urlCache, fileID)
	d.urlMu.Unlock()
}
