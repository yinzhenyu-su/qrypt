// Package onedrive implements a Microsoft OneDrive backend driver for qrypt.
package onedrive

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parentID = d.resolveID(parentID)
	var out []drive.Entry
	next := d.apiPath(fmt.Sprintf("/items/%s/children?$top=1000&$select=id,name,size,fileSystemInfo,file,folder,parentReference", url.PathEscape(parentID)))
	for next != "" {
		var resp listResp
		if err := d.requestJSON(ctx, http.MethodGet, next, nil, &resp); err != nil {
			return nil, fmt.Errorf("onedrive: list %q: %w", parentID, err)
		}
		for _, item := range resp.Value {
			out = append(out, item.entry(parentID))
		}
		next = resp.NextLink
	}
	return out, nil
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	parentID = d.resolveID(parentID)
	body := map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "rename",
	}
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodPost, d.apiPath(fmt.Sprintf("/items/%s/children", url.PathEscape(parentID))), body, &item); err != nil {
		return drive.Entry{}, fmt.Errorf("onedrive: mkdir %q: %w", name, err)
	}
	return item.entry(parentID), nil
}

func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	if err := d.requestJSON(ctx, http.MethodDelete, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), nil, nil); err != nil {
		return fmt.Errorf("onedrive: remove %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) error {
	body := map[string]any{"name": newName}
	if err := d.requestJSON(ctx, http.MethodPatch, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), body, nil); err != nil {
		return fmt.Errorf("onedrive: rename %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) error {
	dstParentID = d.resolveID(dstParentID)
	body := map[string]any{
		"parentReference": map[string]any{"id": dstParentID},
		"name":            entry.Name,
	}
	if err := d.requestJSON(ctx, http.MethodPatch, d.apiPath(fmt.Sprintf("/items/%s", url.PathEscape(entry.ID))), body, nil); err != nil {
		return fmt.Errorf("onedrive: move %q: %w", entry.ID, err)
	}
	return nil
}

func (d *Driver) itemByPath(ctx context.Context, p string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.metaURL(p), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) itemByID(ctx context.Context, id string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.apiPath(fmt.Sprintf("/items/%s?$select=id,name,size,fileSystemInfo,file,folder,parentReference,@microsoft.graph.downloadUrl", url.PathEscape(id))), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) itemByChildName(ctx context.Context, parentID, name string) (itemResp, error) {
	var item itemResp
	if err := d.requestJSON(ctx, http.MethodGet, d.apiPath(fmt.Sprintf("/items/%s:/%s:?$select=id,name,size,fileSystemInfo,file,folder,parentReference", url.PathEscape(parentID), escapePathSegment(name))), nil, &item); err != nil {
		return itemResp{}, err
	}
	return item, nil
}

func (d *Driver) resolveID(id string) string {
	if id == "" || id == "0" || id == "/" || id == "root" {
		if d.rootID != "" {
			return d.rootID
		}
		return "root"
	}
	return id
}
