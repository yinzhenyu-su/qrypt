// Package p115 implements the 115 cloud drive driver.
package p115

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
)

func (d *Driver) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("115: invalid offset or size")
	}
	rawSize := rawEntrySize(e)
	if !e.IsDir && rawSize > 0 && offset >= rawSize {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err := d.waitLimit(ctx); err != nil {
		return nil, err
	}
	pickCode, err := d.pickCode(ctx, e)
	if err != nil {
		d.setLastError(fmt.Sprintf("115: read pick_code %q: %v", e.ID, err))
		return nil, err
	}
	var info *driver115.DownloadInfo
	err = d.recordSDK(ctx, "download_info", map[string]any{"id": e.ID, "name": e.Name, "offset": offset, "size": size}, func() error {
		var err error
		info, err = d.cl.DownloadWithUA(pickCode, d.userAgent())
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115: download info %q: %v", e.ID, err))
		// The SDK maps errno 50015 (and a few others) to its sentinel, but
		// when the code is unknown it returns an unexpected error carrying
		// the raw body ("文件不存在或已删除。"); classify both.
		if errors.Is(err, driver115.ErrDownloadFileNotExistOrHasDeleted) || strings.Contains(err.Error(), "文件不存在") {
			return nil, fmt.Errorf("%w: %v", drive.ErrNotFound, err)
		}
		return nil, fmt.Errorf("115: download info: %w", err)
	}
	if info == nil || info.Url.Url == "" {
		return nil, fmt.Errorf("115: download info missing url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Url.Url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = info.Header.Clone()
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", d.userAgent())
	}
	if size > 0 {
		end := offset + size - 1
		if rawSize > 0 && end >= rawSize {
			end = rawSize - 1
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	client := d.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		d.setLastError(fmt.Sprintf("115: read %q: %v", e.ID, err))
		return nil, fmt.Errorf("115: read: %w", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       driverutil.URL(req.URL),
			Status:    resp.StatusCode,
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		})
		return d.bandwidthLimiter.LimitDownload(ctx, resp.Body), nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && rawSize > 0 && offset >= rawSize {
		resp.Body.Close()
		return io.NopCloser(strings.NewReader("")), nil
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d.metrics.Record(ctx, drive.MetricEvent{
		Operation: "download",
		Method:    req.Method,
		URL:       driverutil.URL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": drive.Snippet(raw)},
	})
	err = drive.HTTPError("115: read", nil, resp, raw)
	d.setLastError(err.Error())
	return nil, err
}

func (d *Driver) pickCode(ctx context.Context, entry drive.Entry) (string, error) {
	switch f := drive.EntryRawExtra(entry).(type) {
	case driver115.File:
		if f.PickCode != "" {
			return f.PickCode, nil
		}
	case *driver115.File:
		if f != nil && f.PickCode != "" {
			return f.PickCode, nil
		}
	}
	var f *driver115.File
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": entry.ID}, func() error {
		var err error
		f, err = d.cl.GetFile(entry.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	if f == nil || f.PickCode == "" {
		return "", fmt.Errorf("115: file %q missing pick_code", entry.ID)
	}
	return f.PickCode, nil
}

func (d *Driver) waitUploadedFile(ctx context.Context, parentID, name string, source drive.ReadOnlyFileSource) (drive.Entry, error) {
	sha1Hex := ""
	if sum, ok := drive.SourceHash(source, drive.HashSHA1); ok {
		sha1Hex = strings.ToUpper(hex.EncodeToString(sum))
	}
	var last []drive.Entry
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return drive.Entry{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		entries, err := d.List(ctx, parentID)
		if err != nil {
			return drive.Entry{}, err
		}
		last = entries
		for _, entry := range entries {
			if entry.Name != name || entry.IsDir || entry.Size != source.Size() {
				continue
			}
			if sha1Hex == "" || entrySHA1(entry) == sha1Hex {
				return entry, nil
			}
		}
	}
	names := make([]string, len(last))
	for i, entry := range last {
		names[i] = entry.Name
	}
	return drive.Entry{}, fmt.Errorf("115: uploaded file %q not visible after upload; files=%v", name, names)
}

// RemoteHash reports the content SHA1 the 115 API returned when the entry
// was listed (115 computes sha1 of the stored bytes).

func (d *Driver) RemoteHash(_ context.Context, entry drive.Entry) (drive.HashAlgorithm, string, error) {
	sha1 := entrySHA1(entry)
	if sha1 == "" {
		return "", "", drive.ErrUnsupported
	}
	return drive.HashSHA1, sha1, nil
}

func (d *Driver) resolveID(fileID string) string {
	if fileID == "" || fileID == "0" || fileID == "/" {
		return d.rootID
	}
	return fileID
}

func (d *Driver) resolvePathFrom(ctx context.Context, rootID, p string) (string, error) {
	currentID := d.resolveID(rootID)
	p = strings.Trim(p, "/")
	if p == "" {
		return currentID, nil
	}
	for _, segment := range strings.Split(p, "/") {
		entries, err := d.List(ctx, currentID)
		if err != nil {
			return "", err
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir && entry.Name == segment {
				currentID = entry.ID
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%w: directory %q not found under %q", drive.ErrNotFound, segment, p)
		}
	}
	return currentID, nil
}
