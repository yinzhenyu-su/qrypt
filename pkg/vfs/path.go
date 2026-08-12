package vfs

import (
	"errors"
	"path/filepath"
	"strings"
)

// CleanVirtualPath normalizes qrypt virtual paths to absolute slash paths.
func CleanVirtualPath(path string) string {
	path = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(path, "/")))
	if path == "." {
		return "/"
	}
	return path
}

// IsNotFound reports whether err represents a missing virtual or remote path.
// The sentinel chain is the single source of truth: drivers wrap
// drive.ErrNotFound (via drive.HTTPError on 404 responses or explicitly), and
// vfs aliases it. No string matching — a bare "not found" text without the
// sentinel is not classified as missing.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
