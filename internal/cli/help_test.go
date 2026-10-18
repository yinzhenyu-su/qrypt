package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runCommand(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	return out.String()
}

// TestSchemaCommandEmitsStructuredHelp pins the L4 contract: a command's help is
// available as structured data derived from the command tree.
func TestSchemaCommandEmitsStructuredHelp(t *testing.T) {
	t.Parallel()

	out := runCommand(t, "schema", "offline", "fetch")
	var schema commandSchema
	if err := json.Unmarshal([]byte(out), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, out)
	}
	if schema.Path != "qrypt offline fetch" {
		t.Fatalf("schema path = %q", schema.Path)
	}
	names := map[string]bool{}
	for _, flag := range schema.Flags {
		names[flag.Name] = true
	}
	if !names["download-to"] {
		t.Fatalf("common flags missing from schema: %+v", schema.Flags)
	}
	if names["workers"] {
		t.Fatalf("advanced flag must not be in the common flags: %+v", schema.Flags)
	}
	advanced := map[string]bool{}
	for _, flag := range schema.Advanced {
		advanced[flag.Name] = true
	}
	if !advanced["workers"] {
		t.Fatalf("advanced flags missing from schema: %+v", schema.Advanced)
	}
}

// TestSchemaCommandDescribesSubcommands verifies the schema lists a group's
// subcommands as data.
func TestSchemaCommandDescribesSubcommands(t *testing.T) {
	t.Parallel()

	out := runCommand(t, "schema", "fs")
	var schema commandSchema
	if err := json.Unmarshal([]byte(out), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	names := map[string]bool{}
	for _, child := range schema.Commands {
		names[child.Name] = true
	}
	if !names["list"] || !names["get"] {
		t.Fatalf("schema subcommands = %+v", schema.Commands)
	}
}

// TestCompletionScriptsGenerate verifies the CLI can emit completion scripts for
// the common shells.
func TestCompletionScriptsGenerate(t *testing.T) {
	t.Parallel()

	for _, shell := range []string{"bash", "zsh", "fish"} {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			out := runCommand(t, "completion", shell)
			if len(strings.TrimSpace(out)) == 0 {
				t.Fatalf("completion %s produced no script", shell)
			}
		})
	}
}

// TestCommandsAreSelfDescribing pins the contract that every command carries a
// one-line summary, a long description, and examples (a group carries at least
// two so its help orients the user).
func TestCommandsAreSelfDescribing(t *testing.T) {
	t.Parallel()

	var offenders []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		leaf := !cmd.HasSubCommands()
		if strings.TrimSpace(cmd.Short) == "" {
			offenders = append(offenders, cmd.CommandPath()+": missing summary")
		}
		if strings.TrimSpace(cmd.Long) == "" {
			offenders = append(offenders, cmd.CommandPath()+": missing description")
		}
		want := 1
		if !leaf {
			want = 2
		}
		if got := countExamples(cmd.Example); got < want {
			offenders = append(offenders, fmt.Sprintf("%s: has %d examples, want %d", cmd.CommandPath(), got, want))
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(NewRootCommand())
	if len(offenders) > 0 {
		t.Fatalf("commands are not self-describing:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// countExamples counts the non-empty example lines in a cobra Example block.
func countExamples(example string) int {
	count := 0
	for _, line := range strings.Split(example, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// TestCommandGroupsPrintHelpWithoutArgs pins the L1 contract: a command group
// with no subcommand orients the user instead of failing.
func TestCommandGroupsPrintHelpWithoutArgs(t *testing.T) {
	t.Parallel()

	for _, group := range []string{"fs", "offline", "debug", "config", "driver", "control", "probe"} {
		group := group
		t.Run(group, func(t *testing.T) {
			t.Parallel()
			out := runCommand(t, group)
			if !strings.Contains(out, "Usage:") || !strings.Contains(out, "Available Commands") {
				t.Fatalf("%s with no arguments did not print its help:\n%s", group, out)
			}
		})
	}
}

// TestAdvancedFlagsDisclosedProgressively pins the L2 contract: advanced flags
// are hidden by default and revealed by the deep-help form.
func TestAdvancedFlagsDisclosedProgressively(t *testing.T) {
	t.Parallel()

	defaultHelp := runCommand(t, "offline", "fetch", "--help")
	if strings.Contains(defaultHelp, "--workers") || strings.Contains(defaultHelp, "--chunk-size") {
		t.Fatalf("advanced flags must be hidden by default:\n%s", defaultHelp)
	}
	if !strings.Contains(defaultHelp, "--download-to") {
		t.Fatalf("common flags must stay visible:\n%s", defaultHelp)
	}

	deepHelp := runCommand(t, "offline", "fetch", "--help", "--advanced")
	if !strings.Contains(deepHelp, "--workers") || !strings.Contains(deepHelp, "--chunk-size") {
		t.Fatalf("deep help must reveal advanced flags:\n%s", deepHelp)
	}
}

// TestHiddenAdvancedFlagStillWorks verifies progressive disclosure hides a flag
// from help without disabling it.
func TestHiddenAdvancedFlagStillWorks(t *testing.T) {
	t.Parallel()

	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"offline", "fetch", "--chunk-size", "4M", "--mount", "quark"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected fetch without --download-to to fail")
	}
	if strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("hidden flag was rejected as unknown: %v", err)
	}
}
