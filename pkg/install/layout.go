// Package install describes the on-disk layout of a qrypt installation: where
// the real binary lives, and how each platform exposes it on PATH. Unix uses a
// symlink under ~/.local/bin plus a marker block in a shell startup file;
// Windows keeps the binary at %USERPROFILE%\.qrypt\bin\qrypt.exe and adds that
// directory to the user-scope registry PATH. The bootstrap scripts
// (scripts/install.sh and scripts/install.ps1) write this layout and the
// uninstall command reads it back, so both sides must agree on the constants
// here. A drift test in this package also checks that both scripts and
// pkg/selfupdate agree about release asset naming.
package install

import (
	"path/filepath"
	"strings"
)

const (
	// DataDir is the qrypt data root (relative to the user's home).
	DataDir = ".qrypt"

	// RealDir is the private directory (relative to the user's home) that holds
	// the real binary. It is never placed on PATH.
	RealDir = DataDir + "/bin"

	// LinkDir is the directory (relative to the user's home) that is placed on
	// PATH; it holds a symlink to the real binary.
	LinkDir = ".local/bin"

	// MarkerBegin and MarkerEnd delimit the installer's PATH block in a shell
	// startup file, so uninstall can remove exactly that block.
	MarkerBegin = "# >>> qrypt >>>"
	MarkerEnd   = "# <<< qrypt <<<"

	// InstallMarkerName is a provenance file written next to the Windows binary.
	// Windows has no symlink layer to identify a managed install, so the
	// installer uses this file to tell its own binary from a user's copy.
	InstallMarkerName = ".qrypt-install"
)

// PathExport is the line the installer adds to a shell startup file. It keeps
// the literal $HOME so the entry survives a home directory change.
const PathExport = `export PATH="$HOME/.local/bin:$PATH"`

// RealDirPath returns the absolute directory that holds the real binary for a
// home directory.
func RealDirPath(home string) string {
	return filepath.Join(home, filepath.FromSlash(RealDir))
}

// RealBinaryPath returns the absolute path of the installed real binary for a
// home directory.
func RealBinaryPath(home string) string {
	return filepath.Join(RealDirPath(home), BinaryName)
}

// MarkerPath returns the absolute path of the provenance marker that records a
// managed Windows install.
func MarkerPath(home string) string {
	return filepath.Join(RealDirPath(home), InstallMarkerName)
}

// LinkPath returns the absolute path of the PATH symlink for a home directory.
func LinkPath(home string) string {
	return filepath.Join(home, filepath.FromSlash(LinkDir), BinaryName)
}

// LinkDirPath returns the absolute directory that holds the PATH symlink.
func LinkDirPath(home string) string {
	return filepath.Join(home, filepath.FromSlash(LinkDir))
}

// DataDirPath returns the absolute qrypt data root for a home directory.
func DataDirPath(home string) string {
	return filepath.Join(home, DataDir)
}

// PathBlock returns the marker-delimited block the installer appends, ending
// with a newline.
func PathBlock() string {
	return MarkerBegin + "\n" + PathExport + "\n" + MarkerEnd + "\n"
}

// HasPathBlock reports whether content already carries the installer's block.
func HasPathBlock(content string) bool {
	return strings.Contains(content, MarkerBegin)
}

// StripPathBlock removes the marker-delimited block, inclusive, from content
// and reports whether a block was present. Lines outside the block are left
// byte-for-byte unchanged.
func StripPathBlock(content string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	start := -1
	for i, line := range lines {
		if trimEOL(line) == MarkerBegin {
			start = i
			break
		}
	}
	if start < 0 {
		return content, false
	}
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if trimEOL(lines[i]) == MarkerEnd {
			end = i
			break
		}
	}
	if end < 0 {
		return content, false
	}
	kept := make([]string, 0, len(lines))
	kept = append(kept, lines[:start]...)
	kept = append(kept, lines[end+1:]...)
	return strings.Join(kept, ""), true
}

func trimEOL(line string) string {
	return strings.TrimRight(line, "\r\n")
}

// RCFileCandidates returns the shell startup files the installer may edit, in
// the order it prefers them. Uninstall scans all of them for the marker block,
// so it does not need to know which shell the user runs.
func RCFileCandidates(home string) []string {
	return []string{
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".config", "fish", "config.fish"),
	}
}

// UninstallReport describes what an uninstall removed. Remnants are displaced
// binaries an earlier install or upgrade left behind; Deferred names a file that
// could not be removed because it is the running image.
type UninstallReport struct {
	RemovedLink     string   `json:"removed_link,omitempty"`
	RemovedBinary   string   `json:"removed_binary,omitempty"`
	RemovedPath     string   `json:"removed_path,omitempty"`
	RemovedRemnants []string `json:"removed_remnants,omitempty"`
	EditedFiles     []string `json:"edited_files,omitempty"`
	RemovedData     string   `json:"removed_data,omitempty"`
	Deferred        []string `json:"deferred,omitempty"`
}

// Empty reports whether nothing was removed and nothing was deferred.
func (r UninstallReport) Empty() bool {
	return r.RemovedLink == "" && r.RemovedBinary == "" && r.RemovedPath == "" &&
		len(r.RemovedRemnants) == 0 && len(r.EditedFiles) == 0 && r.RemovedData == "" &&
		len(r.Deferred) == 0
}

// UninstallOptions controls what an uninstall removes.
//
// Uninstall itself is platform-specific: Unix removes the symlink, binary, and
// shell PATH block, while Windows removes the binary, its marker, and the
// user-scope registry PATH entry.
type UninstallOptions struct {
	// Purge also removes the user's qrypt data directory.
	Purge bool
	// DataDir overrides the qrypt data root; defaults to <home>/.qrypt.
	DataDir string
}
