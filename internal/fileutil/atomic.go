package fileutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteAtomic writes destination through a temporary file in the same
// directory. Existing destinations are replaced only when force is true.
func WriteAtomic(
	destination string,
	pattern string,
	mode fs.FileMode,
	force bool,
	write func(*os.File) error,
) error {
	if !force {
		if _, err := os.Lstat(destination); err == nil {
			return fmt.Errorf("%s: %w", destination, fs.ErrExist)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(destination), pattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if force {
		return replaceLocalFile(tmpPath, destination)
	}
	return os.Rename(tmpPath, destination)
}
