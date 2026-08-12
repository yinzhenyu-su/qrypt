package util

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AtomicWriteOptions controls the filesystem guarantees used by WriteAtomicWithOptions.
// Callers should request only guarantees their existing persistence contract needs.
type AtomicWriteOptions struct {
	Pattern      string
	Mode         fs.FileMode
	Replace      bool
	CreateParent bool
	ParentMode   fs.FileMode
	SyncFile     bool
	SyncParent   bool
}

// WriteAtomic writes destination through a temporary file in the same
// directory. Existing destinations are replaced only when force is true.
func WriteAtomic(
	destination string,
	pattern string,
	mode fs.FileMode,
	force bool,
	write func(*os.File) error,
) error {
	return WriteAtomicWithOptions(destination, AtomicWriteOptions{
		Pattern: pattern,
		Mode:    mode,
		Replace: force,
	}, write)
}

// WriteAtomicWithOptions writes destination through a temporary file in the
// same directory, then renames it into place. The destination is never exposed
// with partially written contents.
func WriteAtomicWithOptions(destination string, opts AtomicWriteOptions, write func(*os.File) error) error {
	if destination == "" {
		return fmt.Errorf("fileutil: destination required")
	}
	if write == nil {
		return fmt.Errorf("fileutil: write callback required")
	}
	dir := filepath.Dir(destination)
	if opts.CreateParent {
		mode := opts.ParentMode
		if mode == 0 {
			mode = 0o755
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
	}
	if !opts.Replace {
		if _, err := os.Lstat(destination); err == nil {
			return fmt.Errorf("%s: %w", destination, fs.ErrExist)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	pattern := opts.Pattern
	if pattern == "" {
		pattern = ".qrypt-*"
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if err := tmp.Chmod(opts.Mode); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if opts.SyncFile {
		if err := tmp.Sync(); err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if opts.Replace {
		err = replaceLocalFile(tmpPath, destination)
	} else {
		err = os.Rename(tmpPath, destination)
	}
	if err != nil {
		return err
	}
	if opts.SyncParent {
		return syncParentDirectory(dir)
	}
	return nil
}
