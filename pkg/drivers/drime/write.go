package drime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/drive"
	"github.com/yinzhenyu/qrypt/pkg/logging"
)

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	before := 0
	if entry.IsDir {
		if children, err := d.listChildren(ctx, entry.ID); err == nil {
			before = len(children)
		}
	}
	updated, err := d.renameEntry(ctx, entry, newName)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	if entry.IsDir && before > 0 {
		d.waitForChildren(ctx, entry.ID, before)
	}
	return updated, nil
}

func (d *Driver) renameEntry(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	remote, err := encodeRemoteName(newName)
	if err != nil {
		return drive.Entry{}, err
	}
	resp, err := d.cl.doJSON(ctx, http.MethodPut, "/file-entries/"+url.PathEscape(entry.ID)+"?workspaceId="+url.QueryEscape(d.workspaceID), map[string]any{
		"name": remote,
	}, true)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: rename %q: %w", entry.Name, err)
	}
	if !resp.ok() {
		return drive.Entry{}, resp.error("drime: rename")
	}
	var body struct {
		FileEntry *apiEntry `json:"fileEntry"`
	}
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode rename response: %w", err)
	}
	if body.FileEntry == nil {
		entry.Name = newName
		return entry, nil
	}
	updated := body.FileEntry.entry(entry.ParentID)
	if updated.ParentID == "" {
		updated.ParentID = entry.ParentID
	}
	return updated, nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error) {
	dst := d.resolveID(dstParentID)
	before := 0
	if entry.IsDir {
		if children, err := d.listChildren(ctx, entry.ID); err == nil {
			before = len(children)
		}
	}
	var destination any
	if dst != "" {
		destination = json.Number(dst)
	}
	resp, err := d.cl.post(ctx, "/file-entries/move?workspaceId="+url.QueryEscape(d.workspaceID), map[string]any{
		"entryIds":      []string{entry.ID},
		"destinationId": destination,
	}, nil)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("drime: move %q: %w", entry.Name, err)
	}
	var body apiMoveResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode move response: %w", err)
	}
	var updated drive.Entry
	if len(body.Entries) > 0 {
		updated = body.Entries[0].entry(dst)
	} else {
		updated, err = d.getEntry(ctx, entry.ID, dst)
		if err != nil {
			return drive.Entry{}, fmt.Errorf("drime: move %q: %w", entry.Name, err)
		}
	}
	updated.ParentID = dst
	if entry.IsDir && before > 0 {
		d.waitForChildren(ctx, entry.ID, before)
	}
	return updated, nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	forever := d.hardDelete
	resp, err := d.cl.doJSON(ctx, http.MethodPost, "/file-entries/delete", map[string]any{
		"entryIds":      []string{entry.ID},
		"deleteForever": forever,
	}, true)
	if err != nil {
		d.setLastError(err)
		return fmt.Errorf("drime: remove %q: %w", entry.Name, err)
	}
	if !resp.ok() {
		var opts []drive.HTTPErrorOption
		if resp.status() == http.StatusUnprocessableEntity {
			// This endpoint reports an unknown entry id as 422 with an
			// entryIds field; every other 422 stays invalid-input.
			var body apiErrorBody
			_ = json.Unmarshal(resp.body, &body)
			if len(body.Errors["entryIds"]) > 0 {
				opts = append(opts, drive.HTTPErrorStatusAsNotFound(http.StatusUnprocessableEntity))
			}
		}
		return resp.error("drime: remove", opts...)
	}
	var body apiDeleteResponse
	_ = json.Unmarshal(resp.body, &body)
	if entry.IsDir && body.Marked == 0 {
		// One observation showed a directory delete succeeding without marking
		// while a child was still settling. Re-check and retry once.
		time.Sleep(300 * time.Millisecond)
		if children, err := d.listChildren(ctx, entry.ParentID); err == nil {
			for _, child := range children {
				if child.ID == entry.ID {
					_, retryErr := d.cl.post(ctx, "/file-entries/delete", map[string]any{
						"entryIds":      []string{entry.ID},
						"deleteForever": forever,
					}, nil)
					if retryErr != nil && !errors.Is(retryErr, drive.ErrNotFound) {
						return fmt.Errorf("drime: remove %q: %w", entry.Name, retryErr)
					}
					return nil
				}
			}
		}
	}
	return nil
}

func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, fmt.Errorf("drime: copy directory: %w", drive.ErrUnsupported)
	}
	if err := validateName(dstName); err != nil {
		return drive.Entry{}, err
	}
	dst := d.resolveID(dstParentID)
	var destination any
	if dst != "" {
		destination = json.Number(dst)
	}
	resp, err := d.cl.post(ctx, "/file-entries/duplicate?workspaceId="+url.QueryEscape(d.workspaceID), map[string]any{
		"entryIds":      []string{src.ID},
		"destinationId": destination,
	}, nil)
	if err != nil {
		d.setLastError(err)
		return drive.Entry{}, fmt.Errorf("drime: copy %q: %w", src.Name, err)
	}
	var body apiMoveResponse
	if err := json.Unmarshal(resp.body, &body); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode copy response: %w", err)
	}
	if len(body.Entries) == 0 {
		return drive.Entry{}, fmt.Errorf("drime: copy %q: provider returned no entry: %w", src.Name, drive.ErrInvalidInput)
	}
	updated := body.Entries[0].entry(dst)
	updated.ParentID = dst
	if updated.Name == dstName {
		return updated, nil
	}
	renamed, err := d.renameEntry(ctx, updated, dstName)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: copy %q to %q: %w", src.Name, dstName, err)
	}
	return renamed, nil
}

// Stat resolves one entry by its provider id. It lets Core recover private
// metadata without listing the parent directory.
func (d *Driver) Stat(ctx context.Context, id string) (drive.Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return drive.Entry{}, fmt.Errorf("drime: entry id is required: %w", drive.ErrNotFound)
	}
	entry, err := d.getEntry(ctx, id, "")
	if err != nil {
		return drive.Entry{}, err
	}
	return entry, nil
}

func (d *Driver) getEntry(ctx context.Context, id, parentID string) (drive.Entry, error) {
	var resp struct {
		FileEntry *apiEntry `json:"fileEntry"`
	}
	if _, err := d.cl.get(ctx, "/file-entries/"+url.PathEscape(id)+"/model?workspaceId="+url.QueryEscape(d.workspaceID), &resp); err != nil {
		return drive.Entry{}, err
	}
	if resp.FileEntry == nil {
		return drive.Entry{}, fmt.Errorf("drime: entry %q: %w", id, drive.ErrNotFound)
	}
	return resp.FileEntry.entry(parentID), nil
}

func (d *Driver) waitForChildren(ctx context.Context, dirID string, want int) {
	const timeout = 5 * time.Second
	deadline := time.Now().Add(timeout)
	backoff := 150 * time.Millisecond
	for {
		if count, err := d.listChildren(ctx, dirID); err == nil && len(count) >= want {
			return
		}
		if time.Now().After(deadline) {
			logging.L.Warnf("[drime] directory %s listing did not settle at %d children within %s", dirID, want, timeout)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}
