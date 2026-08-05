package baidunetdisk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) Read(ctx context.Context, entry drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("baidu_netdisk: read: negative offset or size")
	}
	if entry.Size > 0 && offset >= entry.Size {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	u, err := d.downloadURL(ctx, entry)
	if err != nil {
		d.setLastError(err)
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", d.downloadUA)
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
	resp, err := d.httpClient.Do(req)
	d.recordHTTP(ctx, "download", req, resp, start, map[string]any{"range": req.Header.Get("Range")}, err)
	if err != nil {
		d.downloadCache.Delete(entry.ID)
		err = fmt.Errorf("baidu_netdisk: read: %w", err)
		d.setLastError(err)
		return nil, err
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && entry.Size > 0 && offset >= entry.Size {
		resp.Body.Close()
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		d.downloadCache.Delete(entry.ID)
		err := fmt.Errorf("baidu_netdisk: read status %d", resp.StatusCode)
		d.setLastError(err)
		return nil, err
	}
	return d.limiter.LimitDownload(ctx, resp.Body), nil
}

func (d *Driver) downloadURL(ctx context.Context, entry drive.Entry) (string, error) {
	if cached, ok := d.downloadCache.Load(entry.ID); ok {
		if item, ok := cached.(cachedDownloadURL); ok && item.URL != "" && time.Now().Before(item.ExpiresAt) {
			return item.URL, nil
		}
		d.downloadCache.Delete(entry.ID)
	}
	fsID := entryFSID(entry)
	if fsID == "" {
		return "", fmt.Errorf("baidu_netdisk: missing fs_id for %q", entry.ID)
	}
	var resp downloadResp
	if err := d.get(ctx, "/xpan/multimedia", map[string]string{
		"method": "filemetas",
		"fsids":  "[" + fsID + "]",
		"dlink":  "1",
	}, &resp); err != nil {
		return "", fmt.Errorf("baidu_netdisk: download url: %w", err)
	}
	if len(resp.List) == 0 || resp.List[0].Dlink == "" {
		return "", fmt.Errorf("baidu_netdisk: download url is empty")
	}
	dlink := resp.List[0].Dlink
	if strings.Contains(dlink, "?") {
		dlink += "&access_token=" + url.QueryEscape(d.accessToken)
	} else {
		dlink += "?access_token=" + url.QueryEscape(d.accessToken)
	}
	redirectURL, err := d.resolveDownloadRedirect(ctx, dlink)
	if err != nil {
		return "", err
	}
	d.downloadCache.Store(entry.ID, cachedDownloadURL{URL: redirectURL, ExpiresAt: time.Now().Add(defaultDownloadTTL - defaultTokenSkew)})
	return redirectURL, nil
}

func (d *Driver) resolveDownloadRedirect(ctx context.Context, u string) (string, error) {
	client := *d.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", d.downloadUA)
	start := time.Now()
	resp, err := client.Do(req)
	d.recordHTTP(ctx, "resolve_download_redirect", req, resp, start, nil, err)
	if err != nil {
		return "", fmt.Errorf("baidu_netdisk: download redirect: %w", err)
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if location == "" {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return u, nil
		}
		return "", fmt.Errorf("baidu_netdisk: download redirect status %d", resp.StatusCode)
	}
	if parsed, err := url.Parse(location); err == nil && !parsed.IsAbs() {
		location = resp.Request.URL.ResolveReference(parsed).String()
	}
	return location, nil
}

func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	return drive.RemoteHashFromExtra(entry, "md5", drive.HashMD5)
}

func (d *Driver) resolvePath(id string) string {
	if id == "" || id == "/" || id == "0" {
		return d.rootPath
	}
	return normalizeDir(id)
}

func (d *Driver) get(ctx context.Context, pathname string, params map[string]string, out any) error {
	return d.request(ctx, http.MethodGet, d.apiBaseURL+pathname, params, nil, out)
}
