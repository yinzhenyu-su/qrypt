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
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
