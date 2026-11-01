package koofr

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func (d *Driver) filesQuery(path string) url.Values {
	query := url.Values{}
	query.Set("path", path)
	return query
}

// Mkdir creates one directory. The provider answers an existing name with
// 409 AlreadyExists; the driver re-resolves the path and returns the existing
// directory (or a conflict when a file holds the name), so mkdir is
// idempotent.
func (d *Driver) Mkdir(ctx context.Context, parentID, name string) (drive.Entry, error) {
	if err := validateName(name); err != nil {
		return drive.Entry{}, err
	}
	parent, err := d.resolveWithin(parentID, "mkdir")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: mkdir: %w", err)
	}
	target := joinPath(parent, name)
	resp, err := d.cl.post(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/folder", d.filesQuery(parent), map[string]any{"name": name}, nil)
	if err != nil {
		// 409 AlreadyExists (or any conflict classification) re-resolves the
		// target so a concurrent or repeated create stays idempotent.
		if isConflict(err) {
			existing, found, lookupErr := d.lookupChild(ctx, parent, name)
			if lookupErr == nil && found {
				if existing.IsDir {
					return existing, nil
				}
				return drive.Entry{}, fmt.Errorf("koofr: mkdir %q: a file already holds the name: %w", name, drive.ErrConflict)
			}
		}
		err = fmt.Errorf("koofr: mkdir %q: %w", name, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	_ = resp
	created, err := d.info(ctx, target)
	if err != nil {
		// The folder exists now; resolve it from the listing instead.
		if existing, found, lookupErr := d.lookupChild(ctx, parent, name); lookupErr == nil && found && existing.IsDir {
			return existing, nil
		}
		return drive.Entry{}, fmt.Errorf("koofr: mkdir %q: %w", name, err)
	}
	if !created.IsDir {
		return drive.Entry{}, fmt.Errorf("koofr: mkdir %q: provider returned a non-directory: %w", name, drive.ErrInvalidInput)
	}
	return created, nil
}

// Rename renames an entry in place. Live verification showed a case-only
// rename of the entry itself (a.bin -> A.bin) succeeds; a target matching a
// different existing entry case-insensitively answers 409 AlreadyExists,
// which maps to ErrConflict.
func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	if err := validateName(newName); err != nil {
		return drive.Entry{}, err
	}
	path, err := d.resolveWithin(entry.ID, "rename")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: rename: %w", err)
	}
	parent := parentPath(path)
	resp, err := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/rename", d.filesQuery(path), map[string]any{"name": newName}, nil)
	if err != nil {
		err = fmt.Errorf("koofr: rename %q to %q: %w", entry.Name, newName, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	_ = resp
	updated, err := d.info(ctx, joinPath(parent, newName))
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: rename %q to %q: %w", entry.Name, newName, err)
	}
	return updated, nil
}

// Move relocates an entry inside the mounted provider mount. Cross-mount
// destinations are impossible by construction (entry ids are paths on this
// mount), and the request pins toMountId to the mounted mount.
func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error) {
	dst, err := d.resolveWithin(dstParentID, "move")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move: %w", err)
	}
	path, err := d.resolveWithin(entry.ID, "move")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move: %w", err)
	}
	if isWithin(dst, path) {
		return drive.Entry{}, fmt.Errorf("koofr: move %q into itself: %w", entry.Name, drive.ErrInvalidInput)
	}
	target := joinPath(dst, entry.Name)
	if target == path {
		// Moving onto the current location is a no-op; sending it would
		// answer 409 AlreadyExists against the entry itself.
		return entry, nil
	}
	_, moveErr := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/move", d.filesQuery(path), map[string]any{
		"toMountId": d.mountID,
		"toPath":    target,
	}, nil)
	if moveErr != nil {
		err := fmt.Errorf("koofr: move %q: %w", entry.Name, moveErr)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	updated, err := d.info(ctx, target)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move %q: %w", entry.Name, err)
	}
	return updated, nil
}

// Copy duplicates a file provider-side. The source mtime is sent explicitly
// because the provider only preserves metadata that arrives with the request.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, fmt.Errorf("koofr: copy directory: %w", drive.ErrUnsupported)
	}
	if err := validateName(dstName); err != nil {
		return drive.Entry{}, err
	}
	dst, err := d.resolveWithin(dstParentID, "copy")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy: %w", err)
	}
	path, err := d.resolveWithin(src.ID, "copy")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy: %w", err)
	}
	target := joinPath(dst, dstName)
	payload := map[string]any{
		"toMountId": d.mountID,
		"toPath":    target,
	}
	if !src.ModTime.IsZero() {
		payload["modified"] = src.ModTime.UnixMilli()
	}
	_, copyErr := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/copy", d.filesQuery(path), payload, nil)
	if copyErr != nil {
		err := fmt.Errorf("koofr: copy %q to %q: %w", src.Name, dstName, copyErr)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	updated, err := d.info(ctx, target)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy %q to %q: %w", src.Name, dstName, err)
	}
	return updated, nil
}

// Remove deletes an entry. The entry's size, mtime, and provider MD5 ride
// along as conditional guards so a concurrent mutation surfaces as a
// conflict instead of deleting the wrong generation. Non-empty directories
// cascade into the provider trash.
func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	path, err := d.resolveWithin(entry.ID, "remove")
	if err != nil {
		return fmt.Errorf("koofr: remove: %w", err)
	}
	if path == d.rootPath {
		return fmt.Errorf("koofr: remove %q: refusing to remove the mount root: %w", entry.ID, drive.ErrInvalidInput)
	}
	query := d.filesQuery(path)
	if !entry.IsDir && entry.Size > 0 {
		query.Set("removeIfSize", fmt.Sprintf("%d", entry.Size))
	}
	if !entry.ModTime.IsZero() {
		query.Set("removeIfModified", fmt.Sprintf("%d", entry.ModTime.UnixMilli()))
	}
	if hash := entryFileHash(entry); hash != "" {
		query.Set("removeIfHash", hash)
	}
	_, rmErr := d.cl.delete(ctx, "/mounts/"+url.PathEscape(d.mountID)+"/files/remove", query, nil)
	if rmErr != nil {
		err := fmt.Errorf("koofr: remove %q: %w", entry.Name, rmErr)
		d.setLastError(err)
		return err
	}
	return nil
}

// isConflict reports whether err carries the stable conflict sentinel.
func isConflict(err error) bool {
	return err != nil && errors.Is(err, drive.ErrConflict)
}

// isWithin reports whether path equals ancestor or lives underneath it.
func isWithin(path, ancestor string) bool {
	if ancestor == "/" {
		return true
	}
	return path == ancestor || (len(path) > len(ancestor) && path[:len(ancestor)] == ancestor && path[len(ancestor)] == '/')
}
