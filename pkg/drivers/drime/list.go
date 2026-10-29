package drime

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parent := d.resolveID(parentID)
	entries, err := d.listChildren(ctx, parent)
	if err != nil {
		d.setLastError(err)
		return nil, fmt.Errorf("drime: list %q: %w", parentID, err)
	}
	return entries, nil
}

func (d *Driver) listChildren(ctx context.Context, parentID string) ([]drive.Entry, error) {
	seen := map[string]bool{}
	var out []drive.Entry
	for page := 1; ; page++ {
		if page > maxListPages {
			return nil, fmt.Errorf("drime: listing %q exceeds %d pages: %w", parentID, maxListPages, drive.ErrInvalidInput)
		}
		params := url.Values{}
		params.Set("workspaceId", d.workspaceID)
		params.Set("perPage", "100")
		params.Set("page", strconv.Itoa(page))
		params.Set("orderBy", "name")
		params.Set("orderDir", "asc")
		if parentID != "" {
			params.Set("folderId", parentID)
		}
		resp, err := d.cl.doJSON(ctx, http.MethodGet, "/drive/file-entries?"+params.Encode(), nil, true)
		if err != nil {
			return nil, err
		}
		if !resp.ok() {
			return nil, resp.error("drime: list")
		}
		var listing apiListing
		if err := json.Unmarshal(resp.body, &listing); err != nil {
			return nil, fmt.Errorf("drime: decode listing: %w", err)
		}
		if len(listing.Data) == 0 {
			break
		}
		added := 0
		for _, item := range listing.Data {
			if item.ID == 0 {
				continue
			}
			id := strconv.FormatInt(item.ID, 10)
			if seen[id] {
				continue
			}
			seen[id] = true
			added++
			out = append(out, item.entry(parentID))
		}
		if added == 0 && len(listing.Data) >= 100 {
			return nil, fmt.Errorf("drime: listing %q repeated page %d: %w", parentID, page, drive.ErrInvalidInput)
		}
		if len(listing.Data) < 100 {
			break
		}
	}
	return out, nil
}

func (d *Driver) lookupChild(ctx context.Context, parentID, name string) (drive.Entry, bool, error) {
	entries, err := d.listChildren(ctx, parentID)
	if err != nil {
		return drive.Entry{}, false, err
	}
	for _, entry := range entries {
		if entry.Name == name {
			return entry, true, nil
		}
	}
	return drive.Entry{}, false, nil
}

func validateName(name string) error {
	_, err := encodeRemoteName(name)
	return err
}

func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	remote, err := encodeRemoteName(name)
	if err != nil {
		return drive.Entry{}, err
	}
	parent := d.resolveID(parentID)
	existing, found, err := d.lookupChild(ctx, parent, name)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("drime: mkdir %q: %w", name, err)
	}
	if found {
		if existing.IsDir {
			return existing, nil
		}
		return drive.Entry{}, fmt.Errorf("drime: mkdir %q: %w", name, fs.ErrExist)
	}
	var parentValue any
	if parent != "" {
		parentValue = json.Number(parent)
	}
	resp, err := d.cl.post(ctx, "/folders?workspaceId="+url.QueryEscape(d.workspaceID), map[string]any{
		"name":     remote,
		"parentId": parentValue,
	}, nil)
	if err != nil {
		// A concurrent writer may have created the folder between the lookup
		// and the create call. Re-check so the caller gets the existing entry.
		if existing, found, lookupErr := d.lookupChild(ctx, parent, name); lookupErr == nil && found {
			if existing.IsDir {
				return existing, nil
			}
			return drive.Entry{}, fmt.Errorf("drime: mkdir %q: %w", name, fs.ErrExist)
		}
		return drive.Entry{}, fmt.Errorf("drime: mkdir %q: %w", name, err)
	}
	var created apiFolderResponse
	if err := json.Unmarshal(resp.body, &created); err != nil {
		return drive.Entry{}, fmt.Errorf("drime: decode mkdir response: %w", err)
	}
	if created.Folder.ID == 0 {
		return drive.Entry{}, fmt.Errorf("drime: mkdir %q: provider returned no folder: %w", name, drive.ErrInvalidInput)
	}
	entry := created.Folder.entry(parent)
	return entry, nil
}
