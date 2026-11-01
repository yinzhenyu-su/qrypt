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
	switch {
	case name == "":
		return fmt.Errorf("koofr: name is empty: %w", drive.ErrInvalidInput)
	case name == "." || name == "..":
		return fmt.Errorf("koofr: name %q is reserved: %w", name, drive.ErrInvalidInput)
	}
	for _, bad := range name {
		if bad == '/' || bad == '\\' || bad == 0 {
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

// ResolveRemoteName reports the stored form of a plain name. Koofr preserves
// names verbatim, so the mapping is the identity; the provider's
// case-insensitive collision behaviour surfaces through write conflicts and
// the canonical casing listings return.
func (d *Driver) ResolveRemoteName(_ context.Context, plainName string) (drive.RemoteNameInfo, error) {
	if err := validateName(plainName); err != nil {
		return drive.RemoteNameInfo{}, err
	}
	return drive.RemoteNameInfo{PlainName: plainName, RemoteName: plainName}, nil
}
