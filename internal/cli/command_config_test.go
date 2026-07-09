package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/internal/config"
)

func TestDirectPasswordFromStdin(t *testing.T) {
	cmd := newConfigExportRclonePasswordCmd()
	cmd.SetIn(strings.NewReader("secret\r\n"))
	if err := cmd.Flags().Set("password-stdin", "true"); err != nil {
		t.Fatal(err)
	}
	password, direct, err := directPasswordFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !direct || password != "secret" {
		t.Fatalf("got password %q, direct %v", password, direct)
	}
}

func TestValidateConfigRejectsDuplicateMountNames(t *testing.T) {
	cfg := &config.Config{Mounts: []config.MountConfig{
		{Name: "local", Type: "localfs", Params: config.ParamMap{"root_path": t.TempDir()}},
		{Name: "local", Type: "localfs", Params: config.ParamMap{"root_path": t.TempDir()}},
	}}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "duplicate mount") {
		t.Fatalf("expected duplicate mount error, got %v", err)
	}
}

func TestValidateConfigRejectsMissingDriverParameters(t *testing.T) {
	cfg := &config.Config{Mounts: []config.MountConfig{
		{Name: "local", Type: "localfs"},
	}}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Fatalf("expected missing root_path error, got %v", err)
	}
}

func TestLocalFSRejectsLegacyRootParameter(t *testing.T) {
	cfg := &config.Config{Mounts: []config.MountConfig{{
		Name:   "local",
		Type:   "localfs",
		Params: config.ParamMap{"root": t.TempDir()},
	}}}
	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Fatalf("expected root_path validation error, got %v", err)
	}
}

func TestGeneratedConfigPassesValidation(t *testing.T) {
	content, err := generateConfigTemplate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestConfigInitCreatesValidStarter(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "qrypt.toml")
	cmd := newConfigInitCmd()
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.RunE(cmd, []string{configPath}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	root := cfg.Mounts[0].Params["root_path"]
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("starter root was not created: %q, %v", root, err)
	}
}

func TestDirectPasswordFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password.txt")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newConfigExportRclonePasswordCmd()
	if err := cmd.Flags().Set("password-file", path); err != nil {
		t.Fatal(err)
	}
	password, direct, err := directPasswordFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !direct || password != "secret" {
		t.Fatalf("got password %q, direct %v", password, direct)
	}
}

func TestDirectPasswordRejectsInvalidFlagCombinations(t *testing.T) {
	cmd := newConfigExportRclonePasswordCmd()
	if err := cmd.Flags().Set("password", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("password-stdin", "true"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := directPasswordFromFlags(cmd); err == nil {
		t.Fatal("expected conflicting password sources to fail")
	}

	cmd = newConfigExportRclonePasswordCmd()
	if err := cmd.Flags().Set("salt", "salt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := directPasswordFromFlags(cmd); err == nil {
		t.Fatal("expected --salt without a password source to fail")
	}
}

func TestExportDirectWithoutPasswordHashReturnsRawPassword(t *testing.T) {
	password, err := exportDirect("secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if password != "secret" {
		t.Fatalf("exportDirect() = %q", password)
	}
}
