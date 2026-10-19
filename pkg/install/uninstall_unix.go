//go:build !windows

package install

import (
	"os"
	"path/filepath"

	"github.com/yinzhenyu/qrypt/pkg/util"
)

// Uninstall reverses a Unix installation rooted at home: it removes the PATH
// symlink (only when it points at the managed binary), the real binary, and the
// installer's PATH block, plus — only with Purge — the data directory. It never
// removes a PATH entry or symlink the installer did not create, and it is safe
// to run when nothing is installed.
func Uninstall(home string, opts UninstallOptions) (UninstallReport, error) {
	var report UninstallReport

	link := LinkPath(home)
	realPath := RealBinaryPath(home)
	if linkIsManaged(link, realPath) {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return report, err
		}
		report.RemovedLink = link
	}

	if err := os.Remove(realPath); err == nil {
		report.RemovedBinary = realPath
	} else if !os.IsNotExist(err) {
		return report, err
	}
	_ = os.Remove(filepath.Dir(realPath)) // only succeeds when now empty

	for _, rc := range RCFileCandidates(home) {
		found, err := stripBlockFromFile(rc)
		if err != nil {
			return report, err
		}
		if found {
			report.EditedFiles = append(report.EditedFiles, rc)
		}
	}

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

// stripBlockFromFile removes the installer's PATH block from rc when present,
// reporting whether it changed anything.
func stripBlockFromFile(rc string) (bool, error) {
	data, err := os.ReadFile(rc)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	stripped, found := StripPathBlock(string(data))
	if !found {
		return false, nil
	}
	info, err := os.Stat(rc)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(rc, stripped, info.Mode().Perm()); err != nil {
		return false, err
	}
	return true, nil
}

func writeFileAtomic(destination, content string, mode os.FileMode) error {
	return util.WriteAtomicWithOptions(destination, util.AtomicWriteOptions{
		Pattern: ".qrypt-rc-*",
		Mode:    mode,
		Replace: true,
	}, func(file *os.File) error {
		_, err := file.WriteString(content)
		return err
	})
}

// linkIsManaged reports whether path is a symlink resolving to target.
func linkIsManaged(path, target string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(path), dest)
	}
	return filepath.Clean(dest) == filepath.Clean(target)
}
