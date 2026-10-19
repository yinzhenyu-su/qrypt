package uninstall

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/yinzhenyu/qrypt/pkg/install"
)

type testRuntime struct{}

type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func (testRuntime) UsageError(_ *cobra.Command, format string, args ...any) error {
	return &codedError{code: 2, err: fmt.Errorf(format, args...)}
}

func (testRuntime) ExitError(code int, err error) error {
	return &codedError{code: code, err: err}
}

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

// makeManagedInstall creates the files an installer would have created.
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

func TestCommandIsSelfDescribing(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(testRuntime{})
	if strings.TrimSpace(cmd.Short) == "" || strings.TrimSpace(cmd.Long) == "" || strings.TrimSpace(cmd.Example) == "" {
		t.Fatalf("command is not self-describing: %+v", cmd)
	}
}

func TestFlagsAreWired(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(testRuntime{})
	if cmd.Flags().Lookup("yes") == nil {
		t.Fatal("--yes is missing")
	}
	purge := cmd.Flags().Lookup("purge")
	if purge == nil {
		t.Fatal("--purge is missing")
	}
	if !purge.Hidden {
		t.Fatal("--purge must be an advanced (hidden) flag")
	}
	if cmd.Flags().Lookup("yes").Hidden {
		t.Fatal("--yes must be a common flag")
	}
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

func TestPrintReportNamesLeftoversAndDeferred(t *testing.T) {
	cmd := NewCommand(testRuntime{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	printReport(cmd, install.UninstallReport{
		RemovedBinary:   `C:\h\.qrypt\bin\qrypt.exe`,
		RemovedRemnants: []string{`C:\h\.qrypt\bin\qrypt.exe.old`},
		Deferred:        []string{`C:\h\.qrypt\bin\qrypt.exe.old.42`},
	})
	got := out.String()
	for _, want := range []string{"removed binary", "removed leftover", "qrypt.exe.old", "could not remove", "qrypt.exe.old.42"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q is missing %q", got, want)
		}
	}
}

func TestHomeLookupFailureUsesRuntimeExitCode(t *testing.T) {
	previous := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { userHomeDir = previous })

	cmd := NewCommand(testRuntime{})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--yes"})
	err := cmd.Execute()
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != 1 {
		t.Fatalf("error = %v, want runtime exit code 1", err)
	}
}
