//go:build !windows

package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutPaths(t *testing.T) {
	t.Parallel()
	home := "/home/tester"
	if got, want := RealBinaryPath(home), "/home/tester/.qrypt/bin/qrypt"; got != want {
		t.Fatalf("RealBinaryPath = %q, want %q", got, want)
	}
	if got, want := LinkPath(home), "/home/tester/.local/bin/qrypt"; got != want {
		t.Fatalf("LinkPath = %q, want %q", got, want)
	}
	if got, want := LinkDirPath(home), "/home/tester/.local/bin"; got != want {
		t.Fatalf("LinkDirPath = %q, want %q", got, want)
	}
	if got, want := DataDirPath(home), "/home/tester/.qrypt"; got != want {
		t.Fatalf("DataDirPath = %q, want %q", got, want)
	}
}

func TestMarkersAndBlock(t *testing.T) {
	t.Parallel()
	if MarkerBegin != "# >>> qrypt >>>" || MarkerEnd != "# <<< qrypt <<<" {
		t.Fatalf("marker constants changed: %q / %q", MarkerBegin, MarkerEnd)
	}
	block := PathBlock()
	for _, want := range []string{MarkerBegin, PathExport, MarkerEnd} {
		if !strings.Contains(block, want) {
			t.Fatalf("block %q missing %q", block, want)
		}
	}
}

func TestStripPathBlock(t *testing.T) {
	t.Parallel()
	original := "export EDITOR=vim\n" + PathBlock() + "alias ll='ls -l'\n"
	stripped, found := StripPathBlock(original)
	if !found {
		t.Fatal("StripPathBlock did not find a block")
	}
	if strings.Contains(stripped, MarkerBegin) || strings.Contains(stripped, MarkerEnd) {
		t.Fatalf("block survived: %q", stripped)
	}
	if !strings.Contains(stripped, "export EDITOR=vim") || !strings.Contains(stripped, "alias ll='ls -l'") {
		t.Fatalf("unrelated lines were lost: %q", stripped)
	}

	without := "export EDITOR=vim\n"
	if _, found := StripPathBlock(without); found {
		t.Fatal("StripPathBlock reported a block in content that has none")
	}
}

func TestHasPathBlock(t *testing.T) {
	t.Parallel()
	if !HasPathBlock(PathBlock()) {
		t.Fatal("HasPathBlock missed a block")
	}
	if HasPathBlock("export PATH=/usr/bin:$PATH\n") {
		t.Fatal("HasPathBlock reported a block where there is none")
	}
}

func TestUninstallRemovesManagedInstall(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	realPath := RealBinaryPath(home)
	link := LinkPath(home)
	writeTestFile(t, realPath, "binary", 0o755)
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zprofile")
	writeTestFile(t, rc, "export EDITOR=vim\n"+PathBlock(), 0o644)
	// User data must survive.
	config := filepath.Join(DataDirPath(home), "qrypt.toml")
	writeTestFile(t, config, "x", 0o600)

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall error: %v", err)
	}
	if report.RemovedLink != link || report.RemovedBinary != realPath {
		t.Fatalf("report = %+v", report)
	}
	if len(report.EditedFiles) != 1 || report.EditedFiles[0] != rc {
		t.Fatalf("edited files = %+v", report.EditedFiles)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("symlink still present")
	}
	if _, err := os.Lstat(realPath); !os.IsNotExist(err) {
		t.Fatal("binary still present")
	}
	body, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), MarkerBegin) {
		t.Fatalf("rc block survived: %q", body)
	}
	if !strings.Contains(string(body), "export EDITOR=vim") {
		t.Fatalf("rc lost unrelated content: %q", body)
	}
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("user data was removed without Purge: %v", err)
	}
}

func TestUninstallPreservesUnrelatedLink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	link := LinkPath(home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink pointing somewhere that is not the managed binary.
	other := filepath.Join(home, "other-qrypt")
	writeTestFile(t, other, "x", 0o755)
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedLink != "" {
		t.Fatalf("uninstall removed an unrelated symlink: %+v", report)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("unrelated symlink was removed: %v", err)
	}
}

func TestUninstallPreservesOwnPathLine(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	rc := filepath.Join(home, ".zprofile")
	// The user already had ~/.local/bin on PATH, so the installer added no block.
	userLine := `export PATH="$HOME/.local/bin:$PATH"` + "\n"
	writeTestFile(t, rc, userLine, 0o644)

	report, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.EditedFiles) != 0 {
		t.Fatalf("uninstall edited a file with no marker block: %+v", report)
	}
	body, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != userLine {
		t.Fatalf("the user's own PATH line changed: %q", body)
	}
}

func TestUninstallIsIdempotent(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	realPath := RealBinaryPath(home)
	link := LinkPath(home)
	writeTestFile(t, realPath, "binary", 0o755)
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Uninstall(home, UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	second, err := Uninstall(home, UninstallOptions{})
	if err != nil {
		t.Fatalf("second Uninstall error: %v", err)
	}
	if !second.Empty() {
		t.Fatalf("second Uninstall removed something: %+v", second)
	}
}

func TestUninstallPurgeRemovesData(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	config := filepath.Join(DataDirPath(home), "qrypt.toml")
	writeTestFile(t, config, "x", 0o600)

	report, err := Uninstall(home, UninstallOptions{Purge: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.RemovedData == "" {
		t.Fatalf("Purge did not report removing data: %+v", report)
	}
	if _, err := os.Stat(DataDirPath(home)); !os.IsNotExist(err) {
		t.Fatal("data directory survived Purge")
	}
}
