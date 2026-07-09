package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfigFlagScope(t *testing.T) {
	root := newRootCmd()
	if root.Flags().Lookup("config") != nil || root.PersistentFlags().Lookup("config") != nil {
		t.Fatal("root command must not define --config")
	}

	mount, _, err := root.Find([]string{"mount"})
	if err != nil {
		t.Fatal(err)
	}
	if mount.Flag("socket") == nil {
		t.Error("mount does not support --socket")
	}

	for _, args := range [][]string{
		{"mount"},
		{"fs", "list"},
		{"config", "show"},
		{"config", "export-rclone-password"},
		{"debug", "journal"},
	} {
		cmd, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if cmd.Flag("config") == nil {
			t.Errorf("%v does not support --config", args)
		}
	}

	initCmd, _, err := root.Find([]string{"config", "init"})
	if err != nil {
		t.Fatal(err)
	}
	if initCmd.Flag("config") != nil {
		t.Error("config init must not accept --config")
	}
	if initCmd.Flag("out") != nil {
		t.Error("config init output path must be positional")
	}

	driver, _, err := root.Find([]string{"driver", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if driver.Flag("config") != nil {
		t.Error("driver list must not support --config")
	}
}

func TestFSConfigFlagPlacement(t *testing.T) {
	for _, args := range [][]string{
		{"fs", "--config", "/tmp/qrypt.toml", "list", "--help"},
		{"fs", "list", "--config", "/tmp/qrypt.toml", "--help"},
	} {
		root := newRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestFindConfigPathPrecedence(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Chdir(cwd)

	legacy := filepath.Join(home, ".qrypt", "qrypt.toml")
	xdg := filepath.Join(home, "xdg", "qrypt", "qrypt.toml")
	writeTestConfigFile(t, legacy)
	if runtime.GOOS != "windows" {
		writeTestConfigFile(t, xdg)
	}

	if got := findConfigPath(); got != legacy {
		t.Fatalf("findConfigPath() = %q, want %q", got, legacy)
	}

	local := filepath.Join(cwd, "qrypt.toml")
	writeTestConfigFile(t, local)
	if got := findConfigPath(); got != "./qrypt.toml" {
		t.Fatalf("findConfigPath() = %q, want ./qrypt.toml", got)
	}

	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if got := findConfigPath(); got != xdg {
			t.Fatalf("findConfigPath() = %q, want %q", got, xdg)
		}
	}
}

func TestCommandConfigPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := &cobra.Command{Use: "test"}
	withConfigFlag(cmd)
	if err := cmd.Flags().Set("config", "~/.qrypt/qrypt.toml"); err != nil {
		t.Fatal(err)
	}
	got, err := commandConfigPath(cmd)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".qrypt", "qrypt.toml")
	if got != want {
		t.Fatalf("commandConfigPath() = %q, want %q", got, want)
	}
}

func writeTestConfigFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
}
