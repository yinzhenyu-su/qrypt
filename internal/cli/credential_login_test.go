package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialLoginMissingExplicitConfigFailsBeforeLogin(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	missing := filepath.Join(dir, "missing.toml")
	cmd := NewRootCommand("qrypt")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"credential", "login", "--type", "quark", "--config", missing})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("login with a missing --config succeeded:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "load config") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want the missing config path", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("missing config was created: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".qrypt")); !os.IsNotExist(statErr) {
		t.Fatalf("login created the home config directory despite the failure: %v", statErr)
	}
}
