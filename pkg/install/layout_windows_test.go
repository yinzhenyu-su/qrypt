//go:build windows

package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsLayoutPaths(t *testing.T) {
	home := `C:\Users\tester`
	if got, want := RealDirPath(home), filepath.Join(home, ".qrypt", "bin"); got != want {
		t.Fatalf("RealDirPath = %q, want %q", got, want)
	}
	if got, want := RealBinaryPath(home), filepath.Join(home, ".qrypt", "bin", "qrypt.exe"); got != want {
		t.Fatalf("RealBinaryPath = %q, want %q", got, want)
	}
	if got, want := MarkerPath(home), filepath.Join(home, ".qrypt", "bin", ".qrypt-install"); got != want {
		t.Fatalf("MarkerPath = %q, want %q", got, want)
	}
	if BinaryName != "qrypt.exe" {
		t.Fatalf("BinaryName = %q, want qrypt.exe", BinaryName)
	}
	if RealDir != ".qrypt/bin" {
		t.Fatalf("RealDir = %q", RealDir)
	}
}

func TestAppendPathEntryIsIdempotent(t *testing.T) {
	dir := `C:\Users\tester\.qrypt\bin`
	existing := `C:\Windows\system32;C:\Tools`

	withDir, changed := appendPathEntry(existing, dir)
	if !changed {
		t.Fatal("appendPathEntry did not add a missing directory")
	}
	if withDir != existing+";"+dir {
		t.Fatalf("appendPathEntry = %q", withDir)
	}
	if _, changed := appendPathEntry(withDir, dir); changed {
		t.Fatal("appendPathEntry added a duplicate entry")
	}
	// Case and a trailing separator are not distinct entries on Windows.
	if _, changed := appendPathEntry(withDir, `c:\users\tester\.qrypt\bin\`); changed {
		t.Fatal("appendPathEntry added an equivalent entry")
	}
	if got, changed := appendPathEntry("", dir); !changed || got != dir {
		t.Fatalf("appendPathEntry on empty value = %q, %v", got, changed)
	}
}

func TestRemovePathEntryPreservesUnrelatedEntries(t *testing.T) {
	dir := `C:\Users\tester\.qrypt\bin`
	value := `C:\Windows\system32;C:\Users\tester\.qrypt\bin;C:\Tools;`

	withoutDir, removed := removePathEntry(value, `c:\users\tester\.qrypt\bin`)
	if !removed {
		t.Fatal("removePathEntry did not remove the target entry")
	}
	if withoutDir != `C:\Windows\system32;C:\Tools;` {
		t.Fatalf("removePathEntry = %q", withoutDir)
	}
	if _, removed := removePathEntry(withoutDir, dir); removed {
		t.Fatal("removePathEntry reported removing an absent entry")
	}
}

func TestUninstallRemovesManagedWindowsInstall(t *testing.T) {
	home := t.TempDir()
	realPath := RealBinaryPath(home)
	marker := MarkerPath(home)
	writeTestFile(t, realPath, "binary", 0o755)
	writeTestFile(t, marker, "managed", 0o644)

	var removedDirs []string
	previous := removeUserPath
	removeUserPath = func(dir string) (bool, error) {
		for _, existing := range removedDirs {
			if existing == dir {
				return false, nil
			}
		}
		removedDirs = append(removedDirs, dir)
		return true, nil
	}
	t.Cleanup(func() { removeUserPath = previous })

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if report.RemovedBinary != realPath {
		t.Fatalf("RemovedBinary = %q, want %q", report.RemovedBinary, realPath)
	}
	if report.RemovedPath != RealDirPath(home) {
		t.Fatalf("RemovedPath = %q, want %q", report.RemovedPath, RealDirPath(home))
	}
	if len(removedDirs) != 1 || removedDirs[0] != RealDirPath(home) {
		t.Fatalf("removed PATH entries = %+v", removedDirs)
	}
	if _, err := os.Lstat(realPath); !os.IsNotExist(err) {
		t.Fatal("binary still present")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("install marker still present")
	}

	second, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("second Uninstall error: %v", err)
	}
	if !second.Empty() {
		t.Fatalf("second Uninstall removed something: %+v", second)
	}
}

func TestUninstallPreservesWindowsDataByDefault(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, RealBinaryPath(home), "binary", 0o755)
	config := filepath.Join(DataDirPath(home), "qrypt.toml")
	writeTestFile(t, config, "x", 0o600)

	previous := removeUserPath
	removeUserPath = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { removeUserPath = previous })

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedData != "" {
		t.Fatalf("uninstall removed data without Purge: %+v", report)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("user data was removed: %v", err)
	}
}

func TestUninstallSweepsUpgradeLeftovers(t *testing.T) {
	home := t.TempDir()
	installDir := RealDirPath(home)
	writeTestFile(t, RealBinaryPath(home), "binary", 0o755)
	writeTestFile(t, filepath.Join(installDir, BinaryName+".old"), "stale", 0o644)
	writeTestFile(t, filepath.Join(installDir, BinaryName+".old.4242"), "stale-again", 0o644)

	previous := removeUserPath
	removeUserPath = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { removeUserPath = previous })

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if len(report.RemovedRemnants) != 2 {
		t.Fatalf("RemovedRemnants = %v, want 2 entries", report.RemovedRemnants)
	}
	if len(report.Deferred) != 0 {
		t.Fatalf("Deferred = %v, want none", report.Deferred)
	}
	if _, err := os.Lstat(installDir); !os.IsNotExist(err) {
		t.Fatalf("install directory survived an empty-after-sweep uninstall: %v", err)
	}
}

func TestUninstallReportsDeferredRunningImage(t *testing.T) {
	home := t.TempDir()
	realPath := RealBinaryPath(home)
	writeTestFile(t, realPath, "binary", 0o755)
	displaced := realPath + ".old.4242"

	previousPath := removeUserPath
	removeUserPath = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { removeUserPath = previousPath })

	// The running image cannot be deleted; the real path renames it aside. The
	// seam stands in for that, which needs an actual running process.
	previousBinary := removeBinary
	removeBinary = func(string) (bool, string, error) { return true, displaced, nil }
	t.Cleanup(func() { removeBinary = previousBinary })

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if report.RemovedBinary != realPath {
		t.Fatalf("RemovedBinary = %q, want %q", report.RemovedBinary, realPath)
	}
	if len(report.Deferred) != 1 || report.Deferred[0] != displaced {
		t.Fatalf("Deferred = %v, want [%s]", report.Deferred, displaced)
	}
	if report.Empty() {
		t.Fatal("a report with a deferred file reads as empty")
	}
}
