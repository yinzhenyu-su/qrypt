//go:build !windows

package uninstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/install"
)

// execute runs the command with a temporary home and stdin.
func execute(t *testing.T, home, stdin string, args ...string) (string, string, error) {
	t.Helper()
	previous := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = previous })

	cmd := NewCommand(testRuntime{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errBuf.String(), err
}

// makeManagedInstall creates the files a Unix installer would have created: the
// real binary, the ~/.local/bin symlink, and the marker-delimited rc block.
func makeManagedInstall(t *testing.T, home string) (link, realPath, rc string) {
	t.Helper()
	realPath = install.RealBinaryPath(home)
	link = install.LinkPath(home)
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, link); err != nil {
		t.Fatal(err)
	}
	rc = filepath.Join(home, ".zprofile")
	if err := os.WriteFile(rc, []byte("export EDITOR=vim\n"+install.PathBlock()), 0o644); err != nil {
		t.Fatal(err)
	}
	return link, realPath, rc
}

func TestUninstallRemovesManagedInstall(t *testing.T) {
	home := t.TempDir()
	link, realPath, rc := makeManagedInstall(t, home)

	out, _, err := execute(t, home, "", "--yes")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if !strings.Contains(out, link) || !strings.Contains(out, realPath) || !strings.Contains(out, rc) {
		t.Fatalf("output does not name what was removed: %q", out)
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
	if strings.Contains(string(body), install.MarkerBegin) {
		t.Fatalf("PATH block survived: %q", body)
	}
}

func TestUninstallAbortsWithoutConfirmation(t *testing.T) {
	home := t.TempDir()
	link, _, _ := makeManagedInstall(t, home)

	out, _, err := execute(t, home, "n\n")
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if !strings.Contains(out, "aborted") {
		t.Fatalf("output = %q, want an abort message", out)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("install was removed despite aborting: %v", err)
	}
}

func TestUninstallIdempotent(t *testing.T) {
	home := t.TempDir()
	makeManagedInstall(t, home)
	if _, _, err := execute(t, home, "", "--yes"); err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, home, "", "--yes")
	if err != nil {
		t.Fatalf("second uninstall error: %v", err)
	}
	if !strings.Contains(out, "nothing to remove") {
		t.Fatalf("second run output = %q", out)
	}
}

func TestUninstallPurgeRemovesData(t *testing.T) {
	home := t.TempDir()
	makeManagedInstall(t, home)
	config := filepath.Join(install.DataDirPath(home), "qrypt.toml")
	if err := os.WriteFile(config, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := execute(t, home, "", "--purge", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed data") {
		t.Fatalf("output does not report data removal: %q", out)
	}
	if _, err := os.Stat(install.DataDirPath(home)); !os.IsNotExist(err) {
		t.Fatal("data directory survived --purge")
	}
}
