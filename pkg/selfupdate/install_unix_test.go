//go:build !windows

package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallPreservesSymlink(t *testing.T) {
	t.Parallel()
	realDir := t.TempDir()
	realPath := filepath.Join(realDir, "qrypt-v0.4.0")
	writeFile(t, realPath, "old-binary", 0o755)

	linkDir := t.TempDir()
	linkPath := filepath.Join(linkDir, "qrypt")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatal(err)
	}

	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("EvalSymlinks(link) = %q, want %q", resolved, want)
	}

	newBinary := filepath.Join(realDir, "new")
	writeFile(t, newBinary, "new-binary", 0o755)
	if err := installBinary(newBinary, resolved); err != nil {
		t.Fatalf("installBinary error: %v", err)
	}

	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced instead of its target")
	}
	throughLink, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(throughLink) != "new-binary" {
		t.Fatalf("reading through the symlink = %q, want new-binary", throughLink)
	}
}
