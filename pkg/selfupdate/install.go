package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ResolveExecutable returns the real path of the running executable with
// symlinks resolved, so replacing a symlinked install updates its target and
// leaves the symlink intact.
func ResolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// A path we cannot resolve is still the path we were started from.
		return exe, nil
	}
	return resolved, nil
}

// EnsureWritable reports whether the process can replace path, without
// escalating privileges. It probes the containing directory, which is what an
// atomic rename needs, and names a runnable command in the error.
func EnsureWritable(path string) error {
	dir := filepath.Dir(path)
	probe, err := os.CreateTemp(dir, ".qrypt-selfupdate-probe-*")
	if err != nil {
		return fmt.Errorf("%w: %s (%v)\n\nNext (run with sufficient privileges):\n  %s",
			ErrNotWritable, path, err, rerunAdvice(path))
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

// installBinary places the extracted binary at target atomically, preserving
// the executable bit. Nothing at target changes unless the swap succeeds.
func installBinary(newBinary, target string) error {
	if err := EnsureWritable(target); err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".qrypt-selfupdate-new-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	src, err := os.Open(newBinary)
	if err != nil {
		tmp.Close()
		return err
	}
	_, copyErr := io.Copy(tmp, src)
	src.Close()
	if copyErr != nil {
		tmp.Close()
		return copyErr
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := swapIntoPlace(tmpPath, target); err != nil {
		return err
	}
	removeTmp = false
	return nil
}
