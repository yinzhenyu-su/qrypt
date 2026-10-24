package util

import (
	"context"
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
// with partially written contents. This compatibility entry point has no
// cancellation; new callers that own a context should use
// WriteAtomicWithOptionsContext.
func WriteAtomicWithOptions(destination string, opts AtomicWriteOptions, write func(*os.File) error) error {
	return writeAtomic(destination, opts, write, nil, defaultAtomicRename)
}

// WriteAtomicWithOptionsContext is WriteAtomicWithOptions with cancellation
// checks around the write and immediately before the final rename. A canceled
// context leaves the destination untouched and removes the temporary file. The
// atomic rename is the commit point: cancellation observed after the final
// pre-rename check does not roll back a committed write.
func WriteAtomicWithOptionsContext(ctx context.Context, destination string, opts AtomicWriteOptions, write func(*os.File) error) error {
	if ctx == nil {
		return fmt.Errorf("fileutil: context required")
	}
	return writeAtomic(destination, opts, write, ctx.Err, defaultAtomicRename)
}

type atomicRenameFunc func(tmpPath, destination string, replace bool) error

func defaultAtomicRename(tmpPath, destination string, replace bool) error {
	if replace {
		return replaceLocalFile(tmpPath, destination)
	}
	return os.Rename(tmpPath, destination)
}

func writeAtomic(destination string, opts AtomicWriteOptions, write func(*os.File) error, ctxErr func() error, rename atomicRenameFunc) error {
	check := func() error {
		if ctxErr == nil {
			return nil
		}
		return ctxErr()
	}
	if err := check(); err != nil {
		return err
	}
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
	if err := check(); err != nil {
		return err
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
	if err := check(); err != nil {
		return err
	}
	if opts.SyncFile {
		if err := tmp.Sync(); err != nil {
			return err
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := rename(tmpPath, destination, opts.Replace); err != nil {
		return err
	}
	if opts.SyncParent {
		return syncParentDirectory(dir)
	}
	return nil
}
