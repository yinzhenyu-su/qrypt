package koofr

import (
	"context"
	"fmt"
	"net/url"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// List returns every direct child of a directory in one provider response.
// Koofr's files/list has no pagination; an empty path would silently list the
// mount root, so the resolved path is always absolute and non-empty.
func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	parent := d.resolveID(parentID)
	if parent == "" || parent[0] != '/' {
		return nil, fmt.Errorf("koofr: list: invalid path %q: %w", parentID, drive.ErrInvalidInput)
	}
	entries, err := d.listDir(ctx, parent)
	if err != nil {
		err = fmt.Errorf("koofr: list %q: %w", parentID, err)
		d.setLastError(err)
		return nil, err
	}
	return entries, nil
}

func (d *Driver) listDir(ctx context.Context, path string) ([]drive.Entry, error) {
	query := url.Values{}
	query.Set("path", path)
	var listing apiFileList
	if _, err := d.cl.get(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/list", query, &listing); err != nil {
		return nil, err
	}
	// Case-insensitive providers cannot hold two names differing only by
	// case; deduplicate defensively so a corrupted listing cannot fork one
	// file into two entries.
	seen := map[string]bool{}
	out := make([]drive.Entry, 0, len(listing.Files))
	for _, item := range listing.Files {
		if item.Name == "" {
			continue
		}
		key := lowerASCII(item.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item.entry(path))
	}
	return out, nil
}

// info fetches one entry's metadata by absolute path.
func (d *Driver) info(ctx context.Context, path string) (drive.Entry, error) {
	if path == "" || path[0] != '/' {
		return drive.Entry{}, fmt.Errorf("koofr: info: invalid path %q: %w", path, drive.ErrInvalidInput)
	}
	query := url.Values{}
	query.Set("path", path)
	var file apiFile
	if _, err := d.cl.get(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/info", query, &file); err != nil {
		return drive.Entry{}, err
	}
	return file.entry(parentPath(path)), nil
}

// lookupChild finds a child by case-insensitive name, returning the
// provider's canonical entry.
func (d *Driver) lookupChild(ctx context.Context, parent, name string) (drive.Entry, bool, error) {
	entries, err := d.listDir(ctx, parent)
	if err != nil {
		return drive.Entry{}, false, err
	}
	lower := lowerASCII(name)
	for _, entry := range entries {
		if lowerASCII(entry.Name) == lower {
			return entry, true, nil
		}
	}
	return drive.Entry{}, false, nil
}

func lowerASCII(s string) string {
	b := []byte(s)
	changed := false
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
			changed = true
		}
	}
	if !changed {
		return s
	}
	return string(b)
}

func baseName(p string) string {
	if i := len(p) - 1; i >= 0 {
		for ; i >= 0; i-- {
			if p[i] == '/' {
				return p[i+1:]
			}
		}
	}
	return p
}
