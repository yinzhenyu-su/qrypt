//go:build windows

package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// copyFile copies src to a fresh dst, used to give the child a private copy of
// the test binary it can replace without disturbing the parent's.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// findDisplaced reports the first "<name>.old" or "<name>.old.<pid>" file in dir.
func findDisplaced(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".old") {
			return filepath.Join(dir, entry.Name()), true
		}
	}
	return "", false
}

// lockAgainstDelete opens path for reading while denying write and delete to
// every other opener, so a rename that would replace it fails with a sharing
// violation — the situation a still-running process puts its own image in.
func lockAgainstDelete(t *testing.T, path string) *os.File {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("hold %s: %v", path, err)
	}
	return os.NewFile(uintptr(h), path)
}

func TestSwapIntoPlaceToleratesLeftover(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt.exe")
	writeFile(t, target, "old-binary", 0o644)

	// Simulate a displaced file left by an earlier upgrade.
	writeFile(t, target+".old", "stale", 0o644)

	newBinary := filepath.Join(dir, "new.exe")
	writeFile(t, newBinary, "new-binary", 0o644)

	if err := swapIntoPlace(newBinary, target); err != nil {
		t.Fatalf("swapIntoPlace error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("target content = %q, want new-binary", got)
	}

	// A second upgrade, with another leftover present, must also succeed.
	writeFile(t, target+".old", "stale-again", 0o644)
	second := filepath.Join(dir, "new2.exe")
	writeFile(t, second, "newer-binary", 0o644)
	if err := swapIntoPlace(second, target); err != nil {
		t.Fatalf("second swapIntoPlace error: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "newer-binary" {
		t.Fatalf("target content after second swap = %q", got)
	}
}

func TestSwapIntoPlaceFallsBackWhenOldIsLocked(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt.exe")
	writeFile(t, target, "old-binary", 0o644)

	// A displaced file from an earlier upgrade is still held by the process
	// that is running from it, so the conventional "<target>.old" name cannot
	// be replaced. The swap must fall back to a unique name rather than fail.
	staleOld := target + ".old"
	writeFile(t, staleOld, "stale-binary", 0o644)
	lock := lockAgainstDelete(t, staleOld)
	defer lock.Close()

	newBinary := filepath.Join(dir, "new.exe")
	writeFile(t, newBinary, "new-binary", 0o644)

	if err := swapIntoPlace(newBinary, target); err != nil {
		t.Fatalf("swapIntoPlace error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("target content = %q, want new-binary", got)
	}
	stale, err := os.ReadFile(staleOld)
	if err != nil {
		t.Fatalf("read the locked .old file: %v", err)
	}
	if string(stale) != "stale-binary" {
		t.Fatalf("the locked .old file changed: %q", stale)
	}
	// A successful swap with the locked destination untouched is the evidence
	// that a unique ".old.<pid>" name was used: without the fallback the swap
	// would have failed on the locked conventional name.
}

func TestSwapIntoPlaceFailsWhenTargetIsLocked(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "qrypt.exe")
	writeFile(t, target, "old-binary", 0o644)
	lock := lockAgainstDelete(t, target)
	defer lock.Close()

	newBinary := filepath.Join(dir, "new.exe")
	writeFile(t, newBinary, "new-binary", 0o644)

	if err := swapIntoPlace(newBinary, target); err == nil {
		t.Fatal("swapIntoPlace succeeded while the target could not be renamed")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "old-binary" {
		t.Fatalf("target changed after a failed swap: %q", got)
	}
}

// TestSwapsItsOwnRunningImage exercises the branch a unit test cannot fake: a
// process replacing the executable it is running from. The parent copies the
// test binary to a private path and runs it as a child; the child installs a
// new binary over its own image and exits.
func TestSwapsItsOwnRunningImage(t *testing.T) {
	if os.Getenv("QRYPT_SWAP_SELF_HELPER") == "1" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "self-swap helper: locate executable:", err)
			os.Exit(3)
		}
		if err := installBinary(os.Getenv("QRYPT_SWAP_SELF_NEW"), self); err != nil {
			fmt.Fprintln(os.Stderr, "self-swap helper:", err)
			os.Exit(3)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	selfPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(dir, "qrypt-self.exe")
	copyFile(t, selfPath, self)

	newBinary := filepath.Join(dir, "qrypt-new.exe")
	writeFile(t, newBinary, "new-binary", 0o755)

	cmd := exec.Command(self, "-test.run=^TestSwapsItsOwnRunningImage$")
	cmd.Env = append(os.Environ(),
		"QRYPT_SWAP_SELF_HELPER=1",
		"QRYPT_SWAP_SELF_NEW="+newBinary,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("self-swap helper failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("running image was not replaced: %q", got)
	}

	// The child could not delete the image it was running from, so the
	// displaced file is left behind for a later upgrade's cleanupDisplaced.
	if path, found := findDisplaced(dir); !found {
		entries, _ := os.ReadDir(dir)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("no displaced file beside the running image; dir = %v", names)
	} else {
		t.Logf("displaced running image left at %s", path)
	}
}
