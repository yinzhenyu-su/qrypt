package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestHelpLadderEndToEnd walks the whole help ladder in one flow: a usage error
// points at a group, the group orients, the command help discloses, deep help
// reveals advanced flags, and the machine-readable form describes the same
// command.
func TestHelpLadderEndToEnd(t *testing.T) {
	t.Parallel()

	// L0: a usage error ends with a runnable next step.
	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"fs", "list", "--bad"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected a usage error for an unknown flag")
	}
	if !strings.Contains(err.Error(), "Next:\n  qrypt fs list --help") {
		t.Fatalf("usage error lacks a runnable next step:\n%v", err)
	}

	// L1: the group overview orients with its subcommands.
	groupHelp := runCommand(t, "fs")
	if !strings.Contains(groupHelp, "Available Commands") || !strings.Contains(groupHelp, "get") {
		t.Fatalf("group help does not orient:\n%s", groupHelp)
	}

	// L2: the command help carries a description and examples.
	cmdHelp := runCommand(t, "fs", "get", "--help")
	if !strings.Contains(cmdHelp, "Examples:") {
		t.Fatalf("command help has no examples:\n%s", cmdHelp)
	}

	// L2 deep: advanced flags appear only in the deep-help form, including
	// inherited ones.
	if defaultHelp := runCommand(t, "fs", "list", "--help"); strings.Contains(defaultHelp, "--bwlimit") {
		t.Fatalf("advanced inherited flag leaked into default help:\n%s", defaultHelp)
	}
	deepHelp := runCommand(t, "fs", "list", "--help", "--advanced")
	if !strings.Contains(deepHelp, "--bwlimit") {
		t.Fatalf("deep help did not reveal the advanced inherited flag:\n%s", deepHelp)
	}

	// L4: the machine-readable form describes the same command.
	schemaOut := runCommand(t, "schema", "fs", "get")
	var schema commandSchema
	if err := json.Unmarshal([]byte(schemaOut), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.Path != "qrypt fs get" || schema.Summary == "" {
		t.Fatalf("schema = %+v", schema)
	}
}
