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
	parent, err := d.resolveTarget(parentID, "mkdir")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: mkdir: %w", err)
	}
	if err := parent.checkWritable("mkdir", false); err != nil {
		return drive.Entry{}, err
	}
	if err := d.checkExtMountNameCollision(parent, "mkdir", name); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	target := joinPath(parent.path, name)
	resp, err := d.cl.post(ctx, "/mounts/"+url.PathEscape(parent.mount.ID)+"/files/folder", d.filesQuery(parent.path), map[string]any{"name": name}, nil)
	if err != nil {
		// 409 AlreadyExists (or any conflict classification) re-resolves the
		// target so a concurrent or repeated create stays idempotent.
		if isConflict(err) {
			existing, found, lookupErr := d.lookupChildOn(ctx, parent, name)
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
	created, err := d.infoOn(ctx, parent, target)
	if err != nil {
		// The folder exists now; resolve it from the listing instead.
		if existing, found, lookupErr := d.lookupChildOn(ctx, parent, name); lookupErr == nil && found && existing.IsDir {
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
// which maps to ErrConflict. External mounts reset the mtime to server time
// on rename (probe-observed provider behavior; the driver reports it as-is).
func (d *Driver) Rename(ctx context.Context, entry drive.Entry, newName string) (drive.Entry, error) {
	if err := validateName(newName); err != nil {
		return drive.Entry{}, err
	}
	t, err := d.resolveTarget(entry.ID, "rename")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: rename: %w", err)
	}
	if err := t.checkWritable("rename", false); err != nil {
		return drive.Entry{}, err
	}
	if t.path == d.rootPath || (t.external && t.path == "/") {
		err := fmt.Errorf("koofr: rename %q: refusing to rename the mount root: %w", entry.ID, drive.ErrInvalidInput)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	parent := parentPath(t.path)
	if err := d.checkExtMountNameCollision(opTarget{mount: t.mount, path: parent, external: t.external}, "rename", newName); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	resp, err := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(t.mount.ID)+"/files/rename", d.filesQuery(t.path), map[string]any{"name": newName}, nil)
	if err != nil {
		err = fmt.Errorf("koofr: rename %q to %q: %w", entry.Name, newName, err)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	_ = resp
	updated, err := d.infoOn(ctx, t, joinPath(parent, newName))
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: rename %q to %q: %w", entry.Name, newName, err)
	}
	return updated, nil
}

// Move relocates an entry, staying inside the provider mounts this driver
// serves. In virtual-root mode source and destination may live on different
// mounts; the provider's cross-mount move (toMountId) carries it. In locked
// mode the destination must resolve on the configured mount.
func (d *Driver) Move(ctx context.Context, entry drive.Entry, dstParentID string) (drive.Entry, error) {
	dst, err := d.resolveTarget(dstParentID, "move")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move: %w", err)
	}
	if err := dst.checkWritable("move", false); err != nil {
		return drive.Entry{}, err
	}
	src, err := d.resolveTarget(entry.ID, "move")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move: %w", err)
	}
	if err := src.checkWritable("move", false); err != nil {
		return drive.Entry{}, err
	}
	if src.path == d.rootPath || (src.external && src.path == "/") {
		err := fmt.Errorf("koofr: move %q: refusing to move the mount root: %w", entry.ID, drive.ErrInvalidInput)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	name := baseName(src.path)
	if err := validateName(name); err != nil {
		return drive.Entry{}, err
	}
	target := joinPath(dst.path, name)
	if err := d.checkExtMountNameCollision(dst, "move", name); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	if src.mount.ID == dst.mount.ID {
		if isWithin(dst.path, src.path) {
			return drive.Entry{}, fmt.Errorf("koofr: move %q into itself: %w", name, drive.ErrInvalidInput)
		}
		if target == src.path {
			// Moving onto the current location is a no-op; sending it would
			// answer 409 AlreadyExists against the entry itself.
			return entry, nil
		}
	}
	_, moveErr := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(src.mount.ID)+"/files/move", d.filesQuery(src.path), map[string]any{
		"toMountId": dst.mount.ID,
		"toPath":    target,
	}, nil)
	if moveErr != nil {
		err := fmt.Errorf("koofr: move %q: %w", name, moveErr)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	updated, err := d.infoOn(ctx, dst, target)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: move %q: %w", name, err)
	}
	return updated, nil
}

// Copy duplicates a file provider-side, optionally across mounts in
// virtual-root mode. The source mtime is sent explicitly because the
// provider only preserves metadata that arrives with the request.
func (d *Driver) Copy(ctx context.Context, src drive.Entry, dstParentID, dstName string) (drive.Entry, error) {
	if src.IsDir {
		return drive.Entry{}, fmt.Errorf("koofr: copy directory: %w", drive.ErrUnsupported)
	}
	if err := validateName(dstName); err != nil {
		return drive.Entry{}, err
	}
	dst, err := d.resolveTarget(dstParentID, "copy")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy: %w", err)
	}
	if err := dst.checkWritable("copy", false); err != nil {
		return drive.Entry{}, err
	}
	if err := d.checkExtMountNameCollision(dst, "copy", dstName); err != nil {
		d.setLastError(err)
		return drive.Entry{}, err
	}
	source, err := d.resolveTarget(src.ID, "copy")
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy: %w", err)
	}
	target := joinPath(dst.path, dstName)
	payload := map[string]any{
		"toMountId": dst.mount.ID,
		"toPath":    target,
	}
	if !src.ModTime.IsZero() {
		payload["modified"] = src.ModTime.UnixMilli()
	}
	_, copyErr := d.cl.putOnce(ctx, "/mounts/"+url.PathEscape(source.mount.ID)+"/files/copy", d.filesQuery(source.path), payload, nil)
	if copyErr != nil {
		err := fmt.Errorf("koofr: copy %q to %q: %w", src.Name, dstName, copyErr)
		d.setLastError(err)
		return drive.Entry{}, err
	}
	updated, err := d.infoOn(ctx, dst, target)
	if err != nil {
		return drive.Entry{}, fmt.Errorf("koofr: copy %q to %q: %w", src.Name, dstName, err)
	}
	return updated, nil
}

// Remove deletes an entry. The entry's size, mtime, and provider MD5 ride
// along as conditional guards so a concurrent mutation surfaces as a
// conflict instead of deleting the wrong generation. Non-empty directories
// cascade into the provider trash. The mount root itself — the locked
// root_path or any external mount root in virtual-root mode — is refused.
func (d *Driver) Remove(ctx context.Context, entry drive.Entry) error {
	t, err := d.resolveTarget(entry.ID, "remove")
	if err != nil {
		return fmt.Errorf("koofr: remove: %w", err)
	}
	if err := t.checkWritable("remove", false); err != nil {
		return err
	}
	root := "/"
	if d.locked {
		root = d.rootPath
	}
	if t.path == root {
		return fmt.Errorf("koofr: remove %q: refusing to remove the mount root: %w", entry.ID, drive.ErrInvalidInput)
	}
	query := d.filesQuery(t.path)
	if !entry.IsDir && entry.Size > 0 {
		query.Set("removeIfSize", fmt.Sprintf("%d", entry.Size))
	}
	if !entry.ModTime.IsZero() {
		query.Set("removeIfModified", fmt.Sprintf("%d", entry.ModTime.UnixMilli()))
	}
	if hash := entryFileHash(entry); hash != "" {
		query.Set("removeIfHash", hash)
	}
	_, rmErr := d.cl.deleteOnce(ctx, "/mounts/"+url.PathEscape(t.mount.ID)+"/files/remove", query, nil)
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
