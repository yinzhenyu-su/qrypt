package uninstall

import (
	"bytes"
	"errors"
	"fmt"
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
