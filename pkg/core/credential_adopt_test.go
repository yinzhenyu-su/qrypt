package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func adoptFixture(t *testing.T) (*config.Config, RuntimeLayout) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: tmp},
		Mounts: []config.MountConfig{{
			Name: "docs",
			Type: "quark",
			Params: config.ParamMap{
				"cookie":    "__uid=account-a; __puus=config",
				"root_path": "/Docs",
				"base_url":  "https://drive.example",
			},
		}},
	}
	layout := NewStorageLayout(cfg, RuntimeLayout{})
	store := drive.NewFileStateStore(filepath.Join(layout.DriverDir, "docs"))
	if err := store.SaveJSON("quark_cookie.json", map[string]any{
		"cookie": "__uid=account-a; __puus=rotated",
	}); err != nil {
		t.Fatal(err)
	}
	return cfg, layout
}

func TestAdoptMountCredential(t *testing.T) {
	cfg, layout := adoptFixture(t)
	adoption, err := AdoptMountCredential(cfg, layout, "docs", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if !adoption.StateAdopted || adoption.Account != "account-a" {
		t.Fatalf("adoption = %+v, want the legacy state adopted for account-a", adoption)
	}
	cred, ok := cfg.CredentialFor("main")
	if !ok {
		t.Fatal("credential was not created")
	}
	if cred.Params["cookie"] == "" {
		t.Fatalf("credential params = %+v, want the cookie moved", cred.Params)
	}
	if _, moved := cred.Params["root_path"]; moved {
		t.Fatalf("mount-scope root_path moved into the credential: %+v", cred.Params)
	}
	mount, _ := cfg.MountByName("docs")
	if mount.Credential != "main" {
		t.Fatalf("mount credential = %q, want main", mount.Credential)
	}
	if _, kept := mount.Params["cookie"]; kept {
		t.Fatalf("mount still carries the credential param: %+v", mount.Params)
	}
	if mount.Params["root_path"] != "/Docs" || mount.Params["base_url"] != "https://drive.example" {
		t.Fatalf("mount params = %+v, want mount-scope values kept", mount.Params)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("adopted config is invalid: %v", err)
	}
}

func TestAdoptMountCredentialRejectsMountWithoutCredentialParams(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Mounts: []config.MountConfig{{
			Name: "local", Type: "localfs", Params: config.ParamMap{"root_path": t.TempDir()},
		}},
	}
	layout := NewStorageLayout(cfg, RuntimeLayout{})
	if _, err := AdoptMountCredential(cfg, layout, "local", "main", false); err == nil {
		t.Fatal("adopting a mount without credential params should fail")
	}
}

func TestPurgeStatePaths(t *testing.T) {
	tmp := t.TempDir()
	layout := NewStorageLayout(&config.Config{Storage: config.StorageConfig{WorkDir: tmp}}, RuntimeLayout{})
	credentialDir := filepath.Join(layout.StateDir, "credentials", "main")
	mountDir := filepath.Join(layout.DriverDir, "docs")
	for _, dir := range []string{credentialDir, mountDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := PurgeCredentialState(layout, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialDir); !os.IsNotExist(err) {
		t.Fatalf("credential state still present: %v", err)
	}
	if err := PurgeMountState(layout, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mountDir); !os.IsNotExist(err) {
		t.Fatalf("mount state still present: %v", err)
	}
	if err := PurgeCredentialState(layout, "../escape"); err == nil {
		t.Fatal("a non-single-component name should be rejected")
	}
}
