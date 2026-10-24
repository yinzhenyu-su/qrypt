package mount

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core"
	"github.com/yinzhenyu/qrypt/pkg/crypt"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/quark"
)

func mountTestRuntime(cfg *config.Config, path string) cliruntime.TestRuntime {
	return cliruntime.TestRuntime{
		CommandConfigFn: func(*cobra.Command) (cliruntime.ConfigState, error) {
			return cliruntime.ConfigState{Path: path, Cfg: cfg}, nil
		},
		WithRuntimeConfigFlagFn: func(cmd *cobra.Command) *cobra.Command { return cmd },
		WithConfigFlagFn:        func(cmd *cobra.Command) *cobra.Command { return cmd },
		UsageErrorFn: func(*cobra.Command, string, ...any) error {
			return &usageError{}
		},
	}
}

type usageError struct{}

func (e *usageError) Error() string { return "usage" }

func runMount(t *testing.T, rt cliruntime.Runtime, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(rt)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestMountAddSetListRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Credentials: []config.CredentialConfig{{
			Name:   "main",
			Type:   "quark",
			Params: config.ParamMap{"cookie": "k=v"},
		}},
		Mounts: []config.MountConfig{{
			Name: "base", Type: "quark", Credential: "main",
			Params: config.ParamMap{"root_path": "/Base"},
		}},
	}
	rt := mountTestRuntime(cfg, path)

	if _, err := runMount(t, rt, "add", "docs", "--credential", "missing"); err == nil {
		t.Fatal("adding a mount with an unknown credential should fail")
	}
	if _, err := runMount(t, rt, "add", "docs", "--credential", "main", "--param", "root_path=/Docs"); err != nil {
		t.Fatalf("add: %v", err)
	}
	added, ok := cfg.MountByName("docs")
	if !ok {
		t.Fatal("mount was not added")
	}
	if added.Type != "quark" || added.Credential != "main" || added.Params["root_path"] != "/Docs" {
		t.Fatalf("added mount = %+v", added)
	}
	if _, err := runMount(t, rt, "add", "docs", "--credential", "main"); err == nil {
		t.Fatal("adding a duplicate mount should fail")
	}
	if _, err := runMount(t, rt, "add", "set", "--credential", "main"); err == nil {
		t.Fatal("reserved mount name should be rejected")
	}
	if _, err := runMount(t, rt, "add", "badsalt", "--credential", "main", "--salt", "s"); err == nil {
		t.Fatal("encryption tuning without --password should fail")
	}
	if _, err := runMount(t, rt, "add", "secure", "--credential", "main", "--password", "secret",
		"--salt", "salt", "--filename-encryption", "obfuscate", "--filename-encoding", "base64", "--content-dedup"); err != nil {
		t.Fatalf("add with encryption: %v", err)
	}
	secure, _ := cfg.MountByName("secure")
	if secure.Encryption == nil || secure.Encryption.Password != "secret" || secure.Encryption.Salt != "salt" ||
		secure.Encryption.FileNameEncryption != "obfuscate" || secure.Encryption.FileNameEncoding != "base64" || !secure.Encryption.ContentDedup {
		t.Fatalf("secure mount encryption = %+v", secure.Encryption)
	}
	if _, err := runMount(t, rt, "add", "badenum", "--credential", "main", "--password", "secret", "--filename-encryption", "bogus"); err == nil {
		t.Fatal("an unsupported filename encryption mode should fail validation")
	}
	passwordFile := filepath.Join(t.TempDir(), "password.txt")
	if err := os.WriteFile(passwordFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runMount(t, rt, "add", "filepass", "--credential", "main", "--password-file", passwordFile); err != nil {
		t.Fatalf("add with password file: %v", err)
	}
	if filepass, _ := cfg.MountByName("filepass"); filepass.Encryption == nil || filepass.Encryption.Password != "from-file" {
		t.Fatalf("filepass encryption = %+v, want the file password", filepass.Encryption)
	}

	if _, err := runMount(t, rt, "set", "secure", "--password", "other"); err == nil {
		t.Fatal("changing encryption without --force should fail")
	}
	if _, err := runMount(t, rt, "set", "secure", "--password", "other", "--force"); err != nil {
		t.Fatalf("set encryption with --force: %v", err)
	}
	secure, _ = cfg.MountByName("secure")
	if secure.Encryption == nil || secure.Encryption.Password != "other" {
		t.Fatalf("encryption password = %+v after set", secure.Encryption)
	}

	if _, err := runMount(t, rt, "set", "secure", "--salt", "changed"); err == nil {
		t.Fatal("changing encryption parameters without --force should fail")
	}
	if _, err := runMount(t, rt, "set", "docs", "--salt", "only-salt"); err == nil {
		t.Fatal("encryption tuning without a password should fail")
	}
	if _, err := runMount(t, rt, "set", "base", "--password", "added"); err != nil {
		t.Fatalf("adding encryption to an unencrypted mount should not require --force: %v", err)
	}
	if base, _ := cfg.MountByName("base"); base.Encryption == nil || base.Encryption.Password != "added" {
		t.Fatalf("base mount encryption = %+v", base.Encryption)
	}

	if _, err := runMount(t, rt, "set", "docs", "--param", "root_path=/Documents"); err != nil {
		t.Fatalf("set: %v", err)
	}
	updated, _ := cfg.MountByName("docs")
	if updated.Params["root_path"] != "/Documents" {
		t.Fatalf("root_path = %q after set", updated.Params["root_path"])
	}

	out, err := runMount(t, rt, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "docs") || !strings.Contains(out, "main") || !strings.Contains(out, "/Documents") {
		t.Fatalf("list output = %q", out)
	}

	layout := core.NewStorageLayout(cfg, core.RuntimeLayout{})
	mountState := filepath.Join(layout.DriverDir, "docs")
	if err := os.MkdirAll(mountState, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runMount(t, rt, "remove", "docs", "--purge"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(mountState); !os.IsNotExist(err) {
		t.Fatalf("mount state still present: %v", err)
	}
	if _, ok := cfg.MountByName("docs"); ok {
		t.Fatal("mount was not removed")
	}

	cfg.Upload.DefaultMount = "base"
	if _, err := runMount(t, rt, "remove", "base"); err == nil {
		t.Fatal("removing upload.default_mount should fail")
	}

	last := &config.Config{
		Storage:     config.StorageConfig{WorkDir: t.TempDir()},
		Credentials: cfg.Credentials,
		Mounts:      []config.MountConfig{{Name: "only", Type: "quark", Credential: "main"}},
	}
	lastRT := mountTestRuntime(last, path)
	if _, err := runMount(t, lastRT, "remove", "only"); err == nil || !strings.Contains(err.Error(), "last mount") {
		t.Fatalf("removing the last mount error = %v, want a last-mount error", err)
	}
}

func TestGuardEncryptionChange(t *testing.T) {
	encrypted := &crypt.Config{Password: "secret"}
	if err := guardEncryptionChange(encrypted, nil, true); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("encrypted to plaintext error = %v, want a plaintext rejection", err)
	}
	if err := guardEncryptionChange(encrypted, &crypt.Config{Password: "other"}, false); err == nil {
		t.Fatal("changing encryption without --force should fail")
	}
	if err := guardEncryptionChange(encrypted, &crypt.Config{Password: "other"}, true); err != nil {
		t.Fatalf("changing encryption with --force: %v", err)
	}
	if err := guardEncryptionChange(nil, encrypted, false); err != nil {
		t.Fatalf("adding encryption should not require --force: %v", err)
	}
}
