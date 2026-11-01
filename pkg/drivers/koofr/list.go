package koofr

import (
	"context"
	"fmt"
	"net/url"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

// List returns every direct child of a directory in one provider response.
// Koofr's files/list has no pagination; an empty path would silently list the
// mount root, so the resolved path is always absolute and non-empty. In
// virtual-root mode the root listing additionally carries one directory per
// cached online external mount.
func (d *Driver) List(ctx context.Context, parentID string) ([]drive.Entry, error) {
	if d.virtualRoot() && (parentID == "" || parentID == "/") {
		entries, err := d.listDirOn(ctx, d.mountID, "/", "/")
		if err != nil {
			err = fmt.Errorf("koofr: list %q: %w", parentID, err)
			d.setLastError(err)
			return nil, err
		}
		for _, mount := range d.extMounts {
			entries = append(entries, drive.Entry{
				ID:       extRootID(mount.ID),
				ParentID: "/",
				Name:     mount.Name,
				IsDir:    true,
			})
		}
		return entries, nil
	}
	t, err := d.resolveTarget(parentID, "list")
	if err != nil {
		return nil, fmt.Errorf("koofr: list: %w", err)
	}
	entries, err := d.listDirOn(ctx, t.mount.ID, t.path, d.idPath(t, t.path))
	if err != nil {
		err = fmt.Errorf("koofr: list %q: %w", parentID, err)
		d.setLastError(err)
		return nil, err
	}
	return entries, nil
}

// listDirOn lists one directory on the given provider mount. idParent is the
// caller-facing parent id, so returned entry ids live in the caller's id
// space (paths on the primary storage, "ext:" ids on external mounts).
func (d *Driver) listDirOn(ctx context.Context, mountID, path, idParent string) ([]drive.Entry, error) {
	query := url.Values{}
	query.Set("path", path)
	var listing apiFileList
	if _, err := d.cl.get(ctx, "/mounts/"+url.PathEscape(mountID)+"/files/list", query, &listing); err != nil {
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
		out = append(out, item.entry(idParent))
	}
	return out, nil
}

// info fetches one entry's metadata by absolute path on the mounted
// (primary) mount.
func (d *Driver) info(ctx context.Context, path string) (drive.Entry, error) {
	return d.infoOn(ctx, opTarget{mount: apiMount{ID: d.mountID}}, path)
}

// infoOn fetches one entry's metadata by absolute path on the target's
// mount, mapping the result back into the caller's id space.
func (d *Driver) infoOn(ctx context.Context, t opTarget, path string) (drive.Entry, error) {
	if path == "" || path[0] != '/' {
		return drive.Entry{}, fmt.Errorf("koofr: info: invalid path %q: %w", path, drive.ErrInvalidInput)
	}
	query := url.Values{}
	query.Set("path", path)
	var file apiFile
	if _, err := d.cl.get(ctx, "/mounts/"+url.PathEscape(t.mount.ID)+"/files/info", query, &file); err != nil {
		return drive.Entry{}, err
	}
	return file.entry(d.idPath(t, parentPath(path))), nil
}

// lookupChild finds a child by case-insensitive name on the target's mount,
// returning the provider's canonical entry in the caller's id space.
func (d *Driver) lookupChildOn(ctx context.Context, t opTarget, name string) (drive.Entry, bool, error) {
	entries, err := d.listDirOn(ctx, t.mount.ID, t.path, d.idPath(t, t.path))
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
