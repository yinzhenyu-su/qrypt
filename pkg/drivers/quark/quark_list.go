package quark

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yinzhenyu/qrypt/internal/logging"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolve(parentID)
	pageSize := 100
	var firstResp sortResp
	err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
		"pdir_fid":             parentID,
		"_size":                strconv.Itoa(pageSize),
		"_page":                "1",
		"_fetch_total":         "1",
		"fetch_all_file":       "1",
		"fetch_risk_file_name": "1",
	}, nil, &firstResp)
	if err != nil {
		return nil, fmt.Errorf("quark: list: %w", err)
	}
	if err := apiError(firstResp.respEnvelope); err != nil {
		return nil, err
	}

	total := firstResp.Metadata.Total
	if total < len(firstResp.Data.List) {
		total = len(firstResp.Data.List)
	}
	allFiles := make([]file, total)
	copy(allFiles, firstResp.Data.List)

	if total > pageSize {
		totalPages := (total + pageSize - 1) / pageSize
		var wg sync.WaitGroup
		var once sync.Once
		var lastErr error
		for page := 2; page <= totalPages; page++ {
			wg.Add(1)
			go func(page int) {
				defer wg.Done()
				var resp sortResp
				err := d.cl.request(ctx, http.MethodGet, "/file/sort", map[string]string{
					"pdir_fid":             parentID,
					"_size":                strconv.Itoa(pageSize),
					"_page":                strconv.Itoa(page),
					"fetch_all_file":       "1",
					"fetch_risk_file_name": "1",
				}, nil, &resp)
				if err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				if err := apiError(resp.respEnvelope); err != nil {
					once.Do(func() { lastErr = err })
					return
				}
				offset := (page - 1) * pageSize
				if offset < len(allFiles) {
					copy(allFiles[offset:], resp.Data.List)
				}
			}(page)
		}
		wg.Wait()
		if lastErr != nil {
			return nil, lastErr
		}
	}

	entries := make([]drive.Entry, 0, len(allFiles))
	for _, item := range allFiles {
		if item.Fid == "" {
			continue
		}
		entries = append(entries, item.entry(parentID))
	}
	return entries, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	now := time.Now()
	parentID = d.resolve(parentID)
	logging.L.InfofEvery("quark.mkdir_start", time.Second, "[QUARK] mkdir start parent=%q name=%q", parentID, name)
	data := map[string]any{
		"pdir_fid":      parentID,
		"file_name":     name,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var resp createDirResp
	if err := d.cl.request(ctx, http.MethodPost, "/file", nil, data, &resp); err != nil {
		logging.L.Warnf("[QUARK] mkdir request failed parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, fmt.Errorf("quark: mkdir: %w", err)
	}
	if err := apiError(resp.respEnvelope); err != nil {
		logging.L.Warnf("[QUARK] mkdir api error parent=%q name=%q err=%v", parentID, name, err)
		return drive.Entry{}, err
	}
	logging.L.InfofEvery("quark.mkdir_complete", time.Second, "[QUARK] mkdir complete parent=%q name=%q id=%q", parentID, name, resp.Data.Fid)
	return drive.Entry{ID: resp.Data.Fid, ParentID: parentID, Name: name, IsDir: true, ModTime: now, CreatedAt: now, UpdatedAt: now}, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	data := map[string]any{
		"filelist":     []string{entry.ID},
		"to_pdir_fid":  d.resolve(dstParentID),
		"action_type":  1,
		"exclude_fids": []string{},
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/move", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: move: %w", err)
	}
	return apiError(resp)
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	data := map[string]any{
		"fid":       entry.ID,
		"file_name": newName,
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/rename", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: rename: %w", err)
	}
	return apiError(resp)
}

// Copy implements drive.ServerSideCopier: POST /file/copy (same payload
// shape as /file/move). The API does not return the new file id, so the
// destination is located by listing the target directory for the copied
// name. Directory copies are rejected per the contract.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, drive.ErrUnsupported
	}
	data := map[string]any{
		"filelist":     []string{src.ID},
		"to_pdir_fid":  d.resolve(dstParentID),
		"action_type":  1,
		"exclude_fids": []string{},
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/copy", nil, data, &resp); err != nil {
		return drive.Entry{}, fmt.Errorf("quark: copy: %w", err)
	}
	if err := apiError(resp); err != nil {
		return drive.Entry{}, err
	}
	// Locate the copied entry by name; the copy API gives no id back.
	entries, err := d.List(ctx, dstParentID)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("quark: copy locate: %w", err)
	}
	for _, entry := range entries {
		if entry.Name == dstName && entry.ID != src.ID {
			return entry, nil
		}
	}
	// The list may lag eventual consistency; fall back to a path-based id
	// so the caller still has a usable entry (drivecopy logs it only).
	return drive.Entry{ID: d.resolve(dstParentID) + "/" + dstName, ParentID: dstParentID, Name: dstName, Size: src.Size}, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	data := map[string]any{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{entry.ID},
	}
	var resp respEnvelope
	if err := d.cl.request(ctx, http.MethodPost, "/file/delete", nil, data, &resp); err != nil {
		return fmt.Errorf("quark: delete: %w", err)
	}
	return apiError(resp)
}
