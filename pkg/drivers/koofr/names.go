package koofr

import (
	"context"
	"fmt"
	unicodeutf8 "unicode/utf8"

	"github.com/yinzhenyu/qrypt/pkg/drive"
)

const maxNameBytes = 255

// validateName enforces the provider's name rules client-side. The probe
// showed the server rejects backslashes and overlong names with 400
// InvalidPath, and that it silently falls back to the multipart part filename
// when a query filename carries invalid UTF-8 — so the driver rejects every
// hazardous name before any request.
func validateName(name string) error {
	switch name {
	case "":
		return fmt.Errorf("koofr: name is empty: %w", drive.ErrInvalidInput)
	case ".", "..":
		return fmt.Errorf("koofr: name %q is reserved: %w", name, drive.ErrInvalidInput)
	}
	for _, bad := range name {
		if bad == '/' || bad == '\\' || bad == 0 || bad < 0x20 || bad == 0x7f {
			return fmt.Errorf("koofr: name %q contains %q: %w", name, string(bad), drive.ErrInvalidInput)
		}
	}
	if !unicodeutf8.ValidString(name) {
		return fmt.Errorf("koofr: name is not valid UTF-8: %w", drive.ErrInvalidInput)
	}
	if len(name) > maxNameBytes {
		return fmt.Errorf("koofr: name is %d bytes, maximum is %d: %w", len(name), maxNameBytes, drive.ErrInvalidInput)
	}
	return nil
}

// ResolveRemoteName reports the stored form of a plain name. Koofr treats
// names case-insensitively, so the requested name is fold-matched against
// the root listing: a hit returns the provider's canonical casing (one
// identity, not a second entry), a miss reports not-found so callers never
// invent a duplicate casing, and an invalid name is still rejected locally.
// In virtual-root mode the root also carries the external-mount directories,
// so a miss on the primary storage fold-matches the cached mount names.
func (d *Driver) ResolveRemoteName(ctx context.Context, plainName string) (drive.RemoteNameInfo, error) {
	if err := validateName(plainName); err != nil {
		return drive.RemoteNameInfo{}, err
	}
	entry, found, err := d.lookupChildOn(ctx, opTarget{mount: apiMount{ID: d.mountID}, path: d.rootPath}, plainName)
	if err != nil {
		return drive.RemoteNameInfo{}, fmt.Errorf("koofr: resolve remote name %q: %w", plainName, err)
	}
	if !found && d.virtualRoot() {
		if mount, ok := d.extMountByName(plainName); ok {
			return drive.RemoteNameInfo{PlainName: plainName, RemoteName: mount.Name}, nil
		}
	}
	if !found {
		return drive.RemoteNameInfo{}, fmt.Errorf("koofr: resolve remote name %q: %w", plainName, drive.ErrNotFound)
	}
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: entry.Name}, nil
}
