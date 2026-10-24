package credential

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
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/localfs"
	_ "github.com/yinzhenyu/qrypt/pkg/drivers/quark"
)

func testRuntime(cfg *config.Config, path string) cliruntime.TestRuntime {
	return cliruntime.TestRuntime{
		CommandConfigFn: func(*cobra.Command) (cliruntime.ConfigState, error) {
			return cliruntime.ConfigState{Path: path, Cfg: cfg}, nil
		},
		WithPersistentConfigFlagFn: func(cmd *cobra.Command) *cobra.Command { return cmd },
		UsageErrorFn: func(_ *cobra.Command, format string, args ...any) error {
			return &usageError{message: format}
		},
	}
}

type usageError struct{ message string }

func (e *usageError) Error() string { return e.message }

func run(t *testing.T, rt cliruntime.Runtime, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(rt)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestCredentialAddListShowSetRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Mounts: []config.MountConfig{
			{Name: "docs", Type: "quark", Credential: "main"},
		},
	}
	rt := testRuntime(cfg, path)

	if _, err := run(t, rt, "add", "main", "--type", "quark", "--param", "cookie=k=v", "--param", "root_path=/wrong"); err == nil {
		t.Fatal("add with a mount-scope parameter should fail validation")
	}
	if _, err := run(t, rt, "add", "main", "--type", "quark", "--param", "cookie=k=v"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, ok := cfg.CredentialFor("main"); !ok {
		t.Fatal("credential was not added")
	}
	if _, err := run(t, rt, "add", "main", "--type", "quark", "--param", "cookie=k=v"); err == nil {
		t.Fatal("adding a duplicate credential should fail")
	}

	out, err := run(t, rt, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "docs") {
		t.Fatalf("list output = %q, want the credential and its mount", out)
	}
	if !strings.Contains(out, "SOURCE") || !strings.Contains(out, "UPDATED") {
		t.Fatalf("list output = %q, want lifecycle columns", out)
	}

	out, err = run(t, rt, "show", "main")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, "k=v") || !strings.Contains(out, "***") {
		t.Fatalf("show output = %q, want the secret masked", out)
	}

	if _, err := run(t, rt, "set", "main", "--param", "cookie=k=v2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	params, err := loaded.ResolveMountParams(loaded.Mounts[0])
	if err != nil {
		t.Fatal(err)
	}
	if params["cookie"] != "k=v2" {
		t.Fatalf("effective cookie = %q, want the updated credential value", params["cookie"])
	}

	if _, err := run(t, rt, "remove", "main"); err == nil {
		t.Fatal("removing a referenced credential should fail")
	}
	cfg.Mounts[0].Credential = ""
	cfg.Mounts[0].Params = config.ParamMap{"cookie": "inline"}
	if _, err := run(t, rt, "remove", "main"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := cfg.CredentialFor("main"); ok {
		t.Fatal("credential was not removed")
	}
}

func TestCredentialAddReadsParamFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Mounts: []config.MountConfig{{
			Name: "docs", Type: "quark", Credential: "main",
		}},
	}
	rt := testRuntime(cfg, path)
	secretFile := filepath.Join(t.TempDir(), "cookie.txt")
	if err := os.WriteFile(secretFile, []byte("k=v-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, rt, "add", "main", "--type", "quark", "--param", "cookie=@"+secretFile); err != nil {
		t.Fatalf("add from file: %v", err)
	}
	cred, ok := cfg.CredentialFor("main")
	if !ok || cred.Params["cookie"] != "k=v-from-file" {
		t.Fatalf("credential = %+v, want the file content without the trailing newline", cred)
	}
}

func TestCredentialAdoptConvertsInlineMount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Mounts: []config.MountConfig{{
			Name: "docs", Type: "quark",
			Params: config.ParamMap{"cookie": "k=v", "root_path": "/Docs"},
		}},
	}
	rt := testRuntime(cfg, path)
	if _, err := run(t, rt, "adopt", "docs", "--name", "main"); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	cred, ok := cfg.CredentialFor("main")
	if !ok || cred.Params["cookie"] != "k=v" {
		t.Fatalf("credential = %+v, want the inline cookie", cred)
	}
	mount, _ := cfg.MountByName("docs")
	if mount.Credential != "main" || mount.Params["root_path"] != "/Docs" {
		t.Fatalf("mount = %+v, want the credential reference and root_path", mount)
	}
	if _, leaked := mount.Params["cookie"]; leaked {
		t.Fatalf("mount still carries the cookie: %+v", mount.Params)
	}
}

func TestCredentialTestCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Credentials: []config.CredentialConfig{{
			Name: "local", Type: "localfs",
		}},
		Mounts: []config.MountConfig{{
			Name: "docs", Type: "quark", Credential: "main",
		}},
	}
	cfg.Credentials = append(cfg.Credentials, config.CredentialConfig{
		Name: "main", Type: "quark", Params: config.ParamMap{"cookie": "k=v"},
	})
	rt := testRuntime(cfg, path)
	if _, err := run(t, rt, "test", "local", "--param", "root_path="+t.TempDir()); err != nil {
		t.Fatalf("credential test: %v", err)
	}
	if _, err := run(t, rt, "test", "main", "--param", "cookie=override"); err == nil {
		t.Fatal("credential test must not override a credential-scope secret")
	}
	if _, err := run(t, rt, "test", "local", "--param", "nonsense=x"); err == nil {
		t.Fatal("credential test must reject unknown parameters")
	}

}

func TestCredentialRemovePurge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Credentials: []config.CredentialConfig{{
			Name: "old", Type: "quark", Params: config.ParamMap{"cookie": "k=v"},
		}},
		Mounts: []config.MountConfig{{
			Name: "docs", Type: "quark", Credential: "main",
		}},
	}
	cfg.Credentials = append(cfg.Credentials, config.CredentialConfig{
		Name: "main", Type: "quark", Params: config.ParamMap{"cookie": "k=v"},
	})
	layout := core.NewStorageLayout(cfg, core.RuntimeLayout{})
	stateDir := filepath.Join(layout.StateDir, "credentials", "old")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "session.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := testRuntime(cfg, path)
	if _, err := run(t, rt, "remove", "old", "--purge"); err != nil {
		t.Fatalf("remove --purge: %v", err)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("credential state still present: %v", err)
	}
}
