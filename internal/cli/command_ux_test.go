package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRootSuppressesUsageForRuntimeErrors(t *testing.T) {
	if !NewRootCommand().SilenceUsage {
		t.Fatal("root command must suppress usage for runtime errors")
	}
}

func TestDriverListJSON(t *testing.T) {
	cmd := newDriverListCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := json.Unmarshal(out.Bytes(), &names); err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("expected registered drivers")
	}
}

func TestDebugRequiredFlags(t *testing.T) {
	root := NewRootCommand()
	root.SetArgs([]string{"debug", "collect"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug collect without --socket to fail")
	}

	root = NewRootCommand()
	root.SetArgs([]string{"debug", "bundle", "--socket", "/tmp/qrypt.sock"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected debug bundle without --out to fail")
	}
}
