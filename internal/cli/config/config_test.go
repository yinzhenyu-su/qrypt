package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qconfig "github.com/yinzhenyu/qrypt/pkg/config"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/all"
)

func TestDirectPasswordFromStdin(t *testing.T) {
	cmd := NewExportRclonePasswordCmd(newTestRuntime())
	cmd.SetIn(strings.NewReader("secret\r\n"))
	if err := cmd.Flags().Set("password-stdin", "true"); err != nil {
		t.Fatal(err)
	}
	password, direct, err := DirectPasswordFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !direct || password != "secret" {
		t.Fatalf("got password %q, direct %v", password, direct)
	}
}

func TestValidateConfigRejectsDuplicateMountNames(t *testing.T) {
	cfg := &qconfig.Config{Mounts: []qconfig.MountConfig{
		{Name: "local", Type: "localfs", Params: qconfig.ParamMap{"root_path": t.TempDir()}},
		{Name: "local", Type: "localfs", Params: qconfig.ParamMap{"root_path": t.TempDir()}},
	}}
	if err := qconfig.Validate(cfg); err == nil || !strings.Contains(err.Error(), "duplicate mount") {
		t.Fatalf("expected duplicate mount error, got %v", err)
	}
}

func TestValidateConfigRejectsMissingDriverParameters(t *testing.T) {
	cfg := &qconfig.Config{Mounts: []qconfig.MountConfig{
		{Name: "local", Type: "localfs"},
	}}
	if err := qconfig.Validate(cfg); err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Fatalf("expected missing root_path error, got %v", err)
	}
}

func TestLocalFSRejectsLegacyRootParameter(t *testing.T) {
	cfg := &qconfig.Config{Mounts: []qconfig.MountConfig{{
		Name:   "local",
		Type:   "localfs",
		Params: qconfig.ParamMap{"root": t.TempDir()},
	}}}
	err := qconfig.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Fatalf("expected root_path validation error, got %v", err)
	}
}

func TestGeneratedConfigPassesValidation(t *testing.T) {
	content, err := GenerateTemplate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := qconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := qconfig.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.WorkDir != "~/.qrypt" {
		t.Fatalf("generated storage work_dir = %q", cfg.Storage.WorkDir)
	}
	if cfg.Storage.ReadCacheDir != "" || cfg.Storage.ThumbnailCacheDir != "" || cfg.Storage.UploadDir != "" || cfg.Storage.StateDir != "" || cfg.Storage.LogDir != "" || cfg.Storage.TmpDir != "" {
		t.Fatalf("generated config should derive storage children from work_dir: %+v", cfg.Storage)
	}
}

func TestConfigInitCreatesValidStarter(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "qrypt.toml")
	cmd := NewInitCmd(newTestRuntime())
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.RunE(cmd, []string{configPath}); err != nil {
		t.Fatal(err)
	}
	cfg, err := qconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := qconfig.Validate(cfg); err != nil {
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
	cmd := NewExportRclonePasswordCmd(newTestRuntime())
	if err := cmd.Flags().Set("password-file", path); err != nil {
		t.Fatal(err)
	}
	password, direct, err := DirectPasswordFromFlags(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !direct || password != "secret" {
		t.Fatalf("got password %q, direct %v", password, direct)
	}
}

func TestDirectPasswordRejectsInvalidFlagCombinations(t *testing.T) {
	cmd := NewExportRclonePasswordCmd(newTestRuntime())
	if err := cmd.Flags().Set("password", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("password-stdin", "true"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DirectPasswordFromFlags(cmd); err == nil {
		t.Fatal("expected conflicting password sources to fail")
	}

	cmd = NewExportRclonePasswordCmd(newTestRuntime())
	if err := cmd.Flags().Set("salt", "salt"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := DirectPasswordFromFlags(cmd); err == nil {
		t.Fatal("expected --salt without a password source to fail")
	}
}

func TestExportDirectWithoutPasswordHashReturnsRawPassword(t *testing.T) {
	password, err := ExportDirect("secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if password != "secret" {
		t.Fatalf("exportDirect() = %q", password)
	}
}
