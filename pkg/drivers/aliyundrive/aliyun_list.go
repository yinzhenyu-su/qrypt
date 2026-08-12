package aliyundrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolveID(parentID)
	var entries []drive.Entry
	marker := ""
	for {
		var resp listResp
		body := map[string]any{
			"drive_id":                d.driveID,
			"fields":                  "*",
			"image_thumbnail_process": "image/resize,w_400/format,jpeg",
			"image_url_process":       "image/resize,w_1920/format,jpeg",
			"limit":                   200,
			"marker":                  marker,
			"order_by":                d.orderBy,
			"order_direction":         d.orderDirection,
			"parent_file_id":          parentID,
			"video_thumbnail_process": "video/snapshot,t_0,f_jpg,ar_auto,w_300",
			"url_expire_sec":          14400,
		}
		if err := d.cl.request(ctx, http.MethodPost, "/v2/file/list", body, &resp); err != nil {
			err = fmt.Errorf("aliyundrive: list drive_id=%q parent_file_id=%q: %w", d.driveID, parentID, err)
			d.setLastError(err)
			return nil, err
		}
		for _, item := range resp.Items {
			if item.FileID == "" {
				continue
			}
			entries = append(entries, item.entry(parentID))
		}
		if resp.NextMarker == "" {
			break
		}
		marker = resp.NextMarker
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	now := time.Now()
	parentID = d.resolveID(parentID)
	var resp createResp
	body := map[string]any{
		"check_name_mode": "refuse",
		"drive_id":        d.driveID,
		"name":            name,
		"parent_file_id":  parentID,
		"type":            "folder",
	}
	if err := d.cl.request(ctx, http.MethodPost, "/adrive/v2/file/createWithFolders", body, &resp); err != nil {
		err = fmt.Errorf("aliyundrive: mkdir drive_id=%q parent_file_id=%q name=%q: %w", d.driveID, parentID, name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	createdAt, updatedAt, modTime := responseTimes(resp.UpdatedAt, resp.CreatedAt, now)
	return drive.Entry{ID: resp.FileID, ParentID: parentID, Name: name, IsDir: true, ModTime: modTime, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	return d.batch(ctx, entry.ID, d.resolveID(dstParentID), "/file/move")
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	body := map[string]any{
		"check_name_mode": "refuse",
		"drive_id":        d.driveID,
		"file_id":         entry.ID,
		"name":            newName,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v3/file/update", body, nil); err != nil {
		return fmt.Errorf("aliyundrive: rename: %w", err)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	body := map[string]any{
		"drive_id": d.driveID,
		"file_id":  entry.ID,
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v2/recyclebin/trash", body, nil); err != nil {
		return fmt.Errorf("aliyundrive: remove: %w", err)
	}
	return nil
}

// Copy implements drive.ServerSideCopier: PDS CopyFile via the batch
// endpoint (same envelope as Move with /file/copy). File copies return the
// new file_id synchronously; folder copies are async and rejected per the
// contract (drivecopy copies files only).
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	var resp batchResp
	body := map[string]any{
		"requests": []map[string]any{{
			"headers": map[string]string{"Content-Type": "application/json"},
			"method":  "POST",
			"id":      src.ID,
			"body": map[string]any{
				"drive_id":          d.driveID,
				"file_id":           src.ID,
				"to_drive_id":       d.driveID,
				"to_parent_file_id": d.resolveID(dstParentID),
				"auto_rename":       false,
			},
			"url": "/file/copy",
		}},
		"resource": "file",
	}
	if err := d.cl.request(ctx, http.MethodPost, "/v3/batch", body, &resp); err != nil {
		err = fmt.Errorf("aliyundrive: batch copy drive_id=%q file_id=%q dst_parent_id=%q: %w", d.driveID, src.ID, dstParentID, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	if len(resp.Responses) == 0 {
		err := fmt.Errorf("aliyundrive: batch copy returned no responses")
		d.setLastError(err)
		return drive.Entry{}, err
	}
	item := resp.Responses[0]
	if item.Status < 200 || item.Status >= 300 {
		err := drive.HTTPError("aliyundrive: batch copy", nil, &http.Response{Status: fmt.Sprintf("%d", item.Status)}, item.Body)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	var copyBody struct {
		FileID string `json:"file_id"`
	}
	_ = json.Unmarshal(item.Body, &copyBody)
	now := time.Now()
	return drive.Entry{ID: copyBody.FileID, ParentID: dstParentID, Name: dstName, Size: src.Size, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
}
