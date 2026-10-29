package p123

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil"
	"github.com/yinzhenyu/qrypt/pkg/drivers/internal/driverutil/httpclient"
)

const listPageLimit = 100

// listMaxPages bounds a single directory listing. 123pan reports Total and a
// Next cursor, so a healthy listing terminates on its own long before this; the
// cap only stops a runaway server loop (or an absurd Total) from blocking the
// mount forever. It is a variable so tests can shrink it, and reaching it is
// reported as an error: silently returning a short listing would make callers
// treat the missing entries as absent and delete or re-upload them.
var listMaxPages = 10000

// list returns every direct child of parentID, following the cursor/Page
// pagination 123pan exposes on /file/list/new.
func (c *client) list(ctx context.Context, parentID string) ([]remoteFile, error) {
	files := make([]remoteFile, 0)
	total := 0
	for page := 1; ; page++ {
		if err := c.waitList(ctx); err != nil {
			return nil, err
		}
		var resp listResp
		query := map[string]string{
			"driveId":              "0",
			"limit":                strconv.Itoa(listPageLimit),
			"next":                 "0",
			"orderBy":              "file_id",
			"orderDirection":       "desc",
			"parentFileId":         parentID,
			"trashed":              "false",
			"SearchData":           "",
			"Page":                 strconv.Itoa(page),
			"OnlyLookAbnormalFile": "0",
			"event":                "homeListFile",
			"operateType":          "4",
			"inDirectSpace":        "false",
		}
		if err := c.request(ctx, http.MethodGet, "/file/list/new", query, nil, &resp); err != nil {
			return nil, err
		}
		files = append(files, resp.Data.InfoList...)
		total = resp.Data.Total
		if len(resp.Data.InfoList) == 0 || resp.Data.Next == "-1" {
			break
		}
		if total > 0 && len(files) >= total {
			break
		}
		if page >= listMaxPages {
			return nil, fmt.Errorf("123: list: pagination safety limit reached after %d pages (%d entries, server total %d)",
				page, len(files), total)
		}
	}
	return files, nil
}

func (c *client) userInfo(ctx context.Context) (userInfoResp, error) {
	var resp userInfoResp
	if err := c.request(ctx, http.MethodGet, "/user/info", nil, nil, &resp); err != nil {
		return userInfoResp{}, err
	}
	return resp, nil
}

// exists reports whether fileID is a live (non-trashed) object. The details
// endpoint is the only 123pan call that distinguishes "trashed" from
// "present"; download_info keeps serving URLs for recycled files.
func (c *client) exists(ctx context.Context, fileID int64) (bool, error) {
	err := c.request(ctx, http.MethodPost, "/restful/goapi/v1/file/details", nil,
		map[string]any{"file_ids": []int64{fileID}}, nil)
	if err != nil {
		if errIsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *client) downloadInfo(ctx context.Context, meta fileMeta) (string, error) {
	var resp downloadInfoResp
	body := map[string]any{
		"driveId":   0,
		"etag":      meta.Etag,
		"fileId":    meta.FileID,
		"fileName":  meta.Name,
		"s3keyFlag": meta.S3KeyFlag,
		"size":      meta.Size,
		"type":      meta.Type,
	}
	if err := c.request(ctx, http.MethodPost, "/file/download_info", nil, body, &resp); err != nil {
		return "", err
	}
	if resp.Data.DownloadURL == "" {
		return "", fmt.Errorf("123: download_info returned an empty url")
	}
	return resp.Data.DownloadURL, nil
}

func (c *client) mkdir(ctx context.Context, parentID int64, name string) (uploadRequestResp, error) {
	var resp uploadRequestResp
	body := map[string]any{
		"driveId":      0,
		"etag":         "",
		"fileName":     name,
		"parentFileId": parentID,
		"size":         0,
		"type":         fileTypeDir,
		"event":        "newCreateFolder",
		"operateType":  1,
	}
	if err := c.request(ctx, http.MethodPost, "/file/upload_request", nil, body, &resp); err != nil {
		return uploadRequestResp{}, err
	}
	return resp, nil
}

func (c *client) rename(ctx context.Context, fileID int64, newName string) (*remoteFile, error) {
	var resp renameResp
	body := map[string]any{"driveId": 0, "fileId": fileID, "fileName": newName}
	if err := c.request(ctx, http.MethodPost, "/file/rename", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.Data.Info, nil
}

func (c *client) move(ctx context.Context, fileID int64, dstParentID int64) (*remoteFile, error) {
	var resp moveResp
	body := map[string]any{
		"fileIdList":   []map[string]any{{"FileId": fileID}},
		"parentFileId": dstParentID,
	}
	if err := c.request(ctx, http.MethodPost, "/file/mod_pid", nil, body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data.Info) > 0 {
		return &resp.Data.Info[0], nil
	}
	return nil, nil
}

func (c *client) trash(ctx context.Context, meta fileMeta) error {
	item := map[string]any{
		"FileId":       meta.FileID,
		"FileName":     meta.Name,
		"Type":         meta.Type,
		"Etag":         meta.Etag,
		"S3KeyFlag":    meta.S3KeyFlag,
		"Size":         meta.Size,
		"ParentFileId": meta.ParentID,
	}
	body := map[string]any{
		"driveId":           0,
		"operation":         true,
		"fileTrashInfoList": []map[string]any{item},
	}
	return c.request(ctx, http.MethodPost, "/file/trash", nil, body, nil)
}

func (c *client) uploadRequest(ctx context.Context, parentID int64, name string, size int64, etag string) (uploadRequestResp, error) {
	var resp uploadRequestResp
	body := map[string]any{
		"driveId":      0,
		"duplicate":    2, // 2 = overwrite an existing same-name file
		"etag":         etag,
		"fileName":     name,
		"parentFileId": parentID,
		"size":         size,
		"type":         fileTypeFile,
	}
	if err := c.request(ctx, http.MethodPost, "/file/upload_request", nil, body, &resp); err != nil {
		return uploadRequestResp{}, err
	}
	return resp, nil
}

// s3Auth returns presigned URLs for a single-part upload. 123pan treats
// partNumberEnd as exclusive, matching the [1,2) window OpenList sends for a
// one-chunk upload.
func (c *client) s3Auth(ctx context.Context, up uploadRequestResp, start, end int) (map[string]string, error) {
	body := map[string]any{
		"StorageNode":     up.Data.StorageNode,
		"bucket":          up.Data.Bucket,
		"key":             up.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        up.Data.UploadID,
	}
	return c.presigned(ctx, "/file/s3_upload_object/auth", body)
}

// s3PreSignedURLs returns presigned URLs for a batch of multipart parts.
func (c *client) s3PreSignedURLs(ctx context.Context, up uploadRequestResp, start, end int) (map[string]string, error) {
	body := map[string]any{
		"bucket":          up.Data.Bucket,
		"key":             up.Data.Key,
		"partNumberEnd":   end,
		"partNumberStart": start,
		"uploadId":        up.Data.UploadID,
		"StorageNode":     up.Data.StorageNode,
	}
	return c.presigned(ctx, "/file/s3_repare_upload_parts_batch", body)
}

func (c *client) presigned(ctx context.Context, path string, body map[string]any) (map[string]string, error) {
	var resp presignedResp
	if err := c.request(ctx, http.MethodPost, path, nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.Data.PreSignedUrls, nil
}

func (c *client) uploadCompleteV2(ctx context.Context, up uploadRequestResp, size int64, isMultipart bool) (*remoteFile, error) {
	var resp uploadCompleteResp
	body := map[string]any{
		"StorageNode": up.Data.StorageNode,
		"bucket":      up.Data.Bucket,
		"fileId":      up.Data.FileID,
		"fileSize":    size,
		"isMultipart": isMultipart,
		"key":         up.Data.Key,
		"uploadId":    up.Data.UploadID,
	}
	if err := c.request(ctx, http.MethodPost, "/file/upload_complete/v2", nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.Data.FileInfo, nil
}

// uploadComplete finalizes an upload that was streamed directly against the
// static S3 credentials 123pan occasionally returns instead of presigned URLs.
func (c *client) uploadComplete(ctx context.Context, uploadFileID int64) error {
	return c.request(ctx, http.MethodPost, "/file/upload_complete", nil, map[string]any{"fileId": uploadFileID}, nil)
}

// putPart uploads one presigned part. The URL is pre-signed, so no auth header
// is attached and the caller must supply the exact byte range.
func (c *client) putPart(ctx context.Context, rawURL string, reader io.ReadSeeker, offset, length int64) error {
	if _, err := reader.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("123: seek part source: %w", err)
	}
	policy := driverutil.RetryPolicy{MaxAttempts: httpMaxRetries + 1}
	var lastReq *http.Request
	resp, err := policy.Do(ctx, func(ctx context.Context, attempt int) (*http.Response, error) {
		if attempt > 0 {
			if _, err := reader.Seek(offset, io.SeekStart); err != nil {
				return nil, drive.NonRetryable(fmt.Errorf("123: seek part source: %w", err))
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, io.LimitReader(reader, length))
		if err != nil {
			return nil, drive.NonRetryable(fmt.Errorf("123: build part request: %w", err))
		}
		req.ContentLength = length
		lastReq = req
		return c.uploadClient.Do(req)
	})
	if err != nil {
		return fmt.Errorf("123: upload part: %w", err)
	}
	data, readErr := httpclient.ReadBody(resp)
	if readErr != nil {
		return fmt.Errorf("123: upload part: read response: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return drive.HTTPError("123: upload part", lastReq, resp, data)
}
