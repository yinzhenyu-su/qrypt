// Package p115open implements the 115 cloud drive driver on the official
// 115 open platform API (Bearer access_token + refresh_token).
//
// The refresh token is obtained by authorizing an app on open.115.com (for
// example by scanning a PKCE device-code QR with the 115 app). The driver
// auto-refreshes the access token and persists rotated tokens in the state
// store, so a static refresh_token in the config stays valid across restarts.
package p115open

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/util"
)

func (d *Driver) Read(ctx context.Context, e drive.Entry, offset, size int64) (io.ReadCloser, error) {
	if offset < 0 || size < 0 {
		return nil, fmt.Errorf("115_open: invalid offset or size")
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
		d.setLastError(fmt.Sprintf("115_open: read pick_code %q: %v", e.ID, err))
		return nil, err
	}
	var urlStr string
	err = d.recordSDK(ctx, "down_url", map[string]any{"id": e.ID, "name": e.Name, "offset": offset, "size": size}, func() error {
		urlStr, err = d.downloadURL(ctx, pickCode, e.ID)
		return err
	})
	if err != nil {
		d.setLastError(fmt.Sprintf("115_open: download info %q: %v", e.ID, err))
		return nil, fmt.Errorf("115_open: download info: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", downloadUserAgent)
	if size > 0 {
		end := offset + size - 1
		if rawSize > 0 && end >= rawSize {
			end = rawSize - 1
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
			Duration:  time.Since(start).String(),
			Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
			Error:     err.Error(),
		})
		d.setLastError(fmt.Sprintf("115_open: read %q: %v", e.ID, err))
		return nil, fmt.Errorf("115_open: read: %w", err)
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		d.metrics.Record(ctx, drive.MetricEvent{
			Operation: "download",
			Method:    req.Method,
			URL:       util.URL(req.URL),
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
		URL:       util.URL(req.URL),
		Status:    resp.StatusCode,
		Duration:  time.Since(start).String(),
		Request:   map[string]any{"id": e.ID, "offset": offset, "size": size, "range": req.Header.Get("Range")},
		Response:  map[string]any{"body_snippet": util.Snippet(raw)},
	})
	err = fmt.Errorf("115_open: read: %s body=%q", resp.Status, util.Snippet(raw))
	d.setLastError(err.Error())
	return nil, err
}

func (d *Driver) downloadURL(ctx context.Context, pickCode, fileID string) (string, error) {
	resp, err := d.cl.DownURL(ctx, pickCode, downloadUserAgent)
	if err != nil {
		return "", err
	}
	if len(resp) == 0 {
		return "", fmt.Errorf("115_open: download info missing url")
	}
	if u, ok := resp[fileID]; ok && u.URL.URL != "" {
		return u.URL.URL, nil
	}
	for _, u := range resp {
		if u.URL.URL != "" {
			return u.URL.URL, nil
		}
	}
	return "", fmt.Errorf("115_open: download info missing url")
}

func (d *Driver) pickCode(ctx context.Context, entry drive.Entry) (string, error) {
	switch f := drive.EntryRawExtra(entry).(type) {
	case sdk.GetFilesResp_File:
		if f.Pc != "" {
			return f.Pc, nil
		}
	case *sdk.GetFilesResp_File:
		if f != nil && f.Pc != "" {
			return f.Pc, nil
		}
	}
	var info *sdk.GetFolderInfoResp
	err := d.recordSDK(ctx, "get_file", map[string]any{"id": entry.ID}, func() error {
		var err error
		info, err = d.cl.GetFolderInfo(ctx, entry.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	if info == nil || info.PickCode == "" {
		return "", fmt.Errorf("115_open: file %q missing pick_code", entry.ID)
	}
	return info.PickCode, nil
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
	return drive.Entry{}, fmt.Errorf("115_open: uploaded file %q not visible after upload; files=%v", name, names)
}
