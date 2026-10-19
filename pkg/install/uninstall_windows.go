//go:build windows

package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// removeUserPath is a seam so Windows tests can exercise Uninstall without
// changing the real user PATH.
var removeUserPath = RemoveUserPath

// removeBinary is a seam so Windows tests can exercise the reporting of a
// binary that had to be renamed aside, which needs a running image.
var removeBinary = removeBinaryFile

// Uninstall reverses a Windows installation rooted at home: it removes the
// displaced binaries an earlier install or upgrade left beside the managed
// binary, the managed binary and its provenance marker, the user-scope PATH
// entry naming the install directory, and — only with Purge — the data
// directory. A binary it cannot delete because it is the running image is
// renamed aside and reported as deferred rather than silently left behind. It
// leaves unrelated registry entries alone and is safe to run when nothing is
// installed.
func Uninstall(home string, opts UninstallOptions) (UninstallReport, error) {
	var report UninstallReport

	realPath := RealBinaryPath(home)
	installDir := RealDirPath(home)

	// Remnants from an earlier upgrade are unlocked by now; clear them first so
	// the install directory can be removed once the binary is gone.
	for _, remnant := range displacedPaths(installDir) {
		if err := os.Remove(remnant); err == nil {
			report.RemovedRemnants = append(report.RemovedRemnants, remnant)
		}
	}

	if removed, displaced, err := removeBinary(realPath); err != nil {
		return report, err
	} else if removed {
		report.RemovedBinary = realPath
		if displaced != "" {
			report.Deferred = append(report.Deferred, displaced)
		}
	}
	if err := os.Remove(MarkerPath(home)); err != nil && !os.IsNotExist(err) {
		return report, err
	}

	if removed, err := removeUserPath(installDir); err != nil {
		return report, fmt.Errorf("remove %s from user PATH: %w", installDir, err)
	} else if removed {
		report.RemovedPath = installDir
	}

	_ = os.Remove(installDir) // only succeeds when now empty

	if opts.Purge {
		dir := opts.DataDir
		if dir == "" {
			dir = DataDirPath(home)
		}
		if err := os.RemoveAll(dir); err == nil {
			report.RemovedData = dir
		}
	}

	return report, nil
}

// displacedPaths returns the files a rename-aside left beside the managed
// binary: its ".old" name and any ".old.<pid>" variants.
func displacedPaths(dir string) []string {
	base := BinaryName + ".old"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if name == base || strings.HasPrefix(name, base+".") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// removeBinaryFile deletes the installed executable and reports the path it was
// renamed to, if any. A running image cannot be deleted on Windows, so it is
// renamed aside and Windows is asked to delete it on the next boot, mirroring
// `qrypt upgrade`.
func removeBinaryFile(path string) (removed bool, displaced string, err error) {
	err = os.Remove(path)
	if err == nil {
		return true, "", nil
	}
	if os.IsNotExist(err) {
		return false, "", nil
	}

	displaced = fmt.Sprintf("%s.old.%d", path, os.Getpid())
	if renameErr := os.Rename(path, displaced); renameErr != nil {
		return false, "", err
	}
	scheduleDelete(displaced)
	return true, displaced, nil
}

// scheduleDelete removes the displaced file now, or asks Windows to delete it
// on the next reboot when it is still locked.
func scheduleDelete(path string) {
	if err := os.Remove(path); err == nil {
		return
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}
