package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestInstallBinaryReplacesTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt")
	writeFile(t, target, "old-binary", 0o755)
	newBinary := filepath.Join(dir, "new")
	writeFile(t, newBinary, "new-binary", 0o644)

	if err := installBinary(newBinary, target); err != nil {
		t.Fatalf("installBinary error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("target content = %q, want new-binary", got)
	}
	if runtime.GOOS != "windows" {
		// Windows has no executable mode bit; the content check above is all
		// the platform can assert.
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("installed binary is not executable: %v", info.Mode())
		}
	}
}

func TestInstallBinaryFailureLeavesTargetUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt")
	writeFile(t, target, "old-binary", 0o755)

	err := installBinary(filepath.Join(dir, "does-not-exist"), target)
	if err == nil {
		t.Fatal("installBinary with a missing source should fail")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old-binary" {
		t.Fatalf("target changed after a failed install: %q", got)
	}
}

func TestInstallBinaryDoesNotLeaveTempFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt")
	writeFile(t, target, "old", 0o755)
	newBinary := filepath.Join(dir, "src-"+filepath.Base(t.Name()))
	writeFile(t, newBinary, "new", 0o755)
	if err := installBinary(newBinary, target); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "qrypt" && strings.HasPrefix(entry.Name(), ".qrypt-selfupdate-") {
			t.Fatalf("left a temp file behind: %s", entry.Name())
		}
	}
}

func TestEnsureWritableErrorNamesRunnableCommand(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("chmod cannot make a directory non-writable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; the read-only directory check cannot fail")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt")
	writeFile(t, target, "binary", 0o755)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := EnsureWritable(target)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("EnsureWritable error = %v, want ErrNotWritable", err)
	}
	if !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("error does not name a runnable command: %v", err)
	}
}
