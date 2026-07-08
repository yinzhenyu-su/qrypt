package osutil

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// OpenRead opens path and seeks to offset.  If size > 0 the returned
// ReadCloser reads at most size bytes; otherwise it reads to EOF.
func OpenRead(path string, offset, size int64) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	if size > 0 {
		return struct {
			io.Reader
			io.Closer
		}{Reader: io.LimitReader(f, size), Closer: f}, nil
	}
	return f, nil
}

// ExpandHome replaces a leading "~" or "~"+separator with the user's home
// directory. Returns path unchanged when UserHomeDir fails or "~" is not
// a prefix.
func ExpandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if len(path) >= 2 && path[0] == '~' && os.IsPathSeparator(path[1]) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// ExpandHomeIfExists is like ExpandHome but also returns path unchanged when
// the path starts with "~" and the home directory expansion would change it
// without the path actually existing. For cases where you only want to expand
// paths that start with ~ and already exist on disk.
//
// Deprecated: prefer ExpandHome.
func ExpandHomeIfExists(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	expanded := ExpandHome(path)
	if _, err := os.Stat(expanded); err == nil {
		return expanded
	}
	return path
}
