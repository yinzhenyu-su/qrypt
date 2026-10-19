package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestUpgradeAdvancedFlagsReveal verifies the upgrade command follows the help
// ladder: advanced flags stay hidden until --advanced is requested.
func TestUpgradeAdvancedFlagsReveal(t *testing.T) {
	t.Parallel()

	def := flagsSection(runCommand(t, "upgrade", "--help"))
	if strings.Contains(def, "--to") || strings.Contains(def, "--force") {
		t.Fatalf("advanced flags leaked into the default help flags:\n%s", def)
	}
	deep := flagsSection(runCommand(t, "upgrade", "--help", "--advanced"))
	if !strings.Contains(deep, "--to") || !strings.Contains(deep, "--force") {
		t.Fatalf("deep help did not reveal the advanced flags:\n%s", deep)
	}
}

// flagsSection isolates the rendered flag list, so a check is not fooled by a
// flag named in a command's description or examples.
func flagsSection(help string) string {
	if i := strings.Index(help, "Flags:"); i >= 0 {
		return help[i:]
	}
	return ""
}

func TestUpgradeUsageErrorExitCode(t *testing.T) {
	t.Parallel()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"upgrade", "--to", "nope"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a usage error for an unparsable --to")
	}
	if code := ExitCode(err); code != ExitUsage {
		t.Fatalf("exit code = %d, want %d\n%v", code, ExitUsage, err)
	}
}

func TestUpgradeRuntimeErrorExitCode(t *testing.T) {
	t.Parallel()

	// The test build reports version "dev", which the command refuses before
	// it touches the network.
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"upgrade"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a runtime error for a development build")
	}
	if code := ExitCode(err); code != ExitFailure {
		t.Fatalf("exit code = %d, want %d\n%v", code, ExitFailure, err)
	}
}
