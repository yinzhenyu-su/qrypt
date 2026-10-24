package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/drive"
)

func migrationFixture(t *testing.T, legacyCookie string) (*config.Config, RuntimeLayout, string) {
	t.Helper()
	tmp := t.TempDir()
	layout := NewStorageLayout(&config.Config{Storage: config.StorageConfig{WorkDir: tmp}}, RuntimeLayout{})
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: tmp},
		Credentials: []config.CredentialConfig{{
			Name:   "main",
			Type:   "quark",
			Params: config.ParamMap{"cookie": "__uid=account-a; __puus=config"},
		}},
		Mounts: []config.MountConfig{{
			Name:       "docs",
			Type:       "quark",
			Credential: "main",
			Params:     config.ParamMap{"root_path": "/Docs"},
		}},
	}
	mountState := filepath.Join(layout.DriverDir, "docs")
	store := drive.NewFileStateStore(mountState)
	if err := store.SaveJSON("quark_cookie.json", map[string]any{"cookie": legacyCookie}); err != nil {
		t.Fatal(err)
	}
	return cfg, layout, filepath.Join(mountState, "quark_cookie.json")
}

func TestMigrateCredentialSessionAdoptsMatchingAccount(t *testing.T) {
	cfg, layout, legacyPath := migrationFixture(t, "__uid=account-a; __puus=rotated")
	result, err := MigrateCredentialSession(cfg, layout, "docs", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Adopted || result.Account != "account-a" {
		t.Fatalf("result = %+v, want an adopted account-a session", result)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy file removed without --delete-legacy: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(layout.StateDir, "credentials", "main", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Account string `json:"account"`
		Data    []byte `json:"data"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	if session.Account != "account-a" || len(session.Data) == 0 {
		t.Fatalf("session file = %+v, want the adopted account", session)
	}
}

func TestMigrateCredentialSessionSkipsAnotherAccount(t *testing.T) {
	cfg, layout, _ := migrationFixture(t, "__uid=account-b; __puus=other")
	result, err := MigrateCredentialSession(cfg, layout, "docs", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Adopted {
		t.Fatalf("result = %+v, want a skipped migration for another account", result)
	}
	if result.Reason == "" {
		t.Fatal("skipped migration should explain why")
	}
	if _, err := os.Stat(filepath.Join(layout.StateDir, "credentials", "main", "session.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected session file created for a mismatched account: %v", err)
	}
}

func TestMigrateCredentialSessionDeletesLegacyOnRequest(t *testing.T) {
	cfg, layout, legacyPath := migrationFixture(t, "__uid=account-a; __puus=rotated")
	result, err := MigrateCredentialSession(cfg, layout, "docs", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Adopted {
		t.Fatalf("result = %+v, want adoption", result)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy file still present after --delete-legacy: %v", err)
	}
}

func TestMigrateCredentialSessionSkipsStateWithoutAccount(t *testing.T) {
	cfg, layout, _ := migrationFixture(t, "__puus=rotated")
	result, err := MigrateCredentialSession(cfg, layout, "docs", "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Adopted {
		t.Fatalf("result = %+v, want a state without an account marker to be skipped", result)
	}
	if result.Reason == "" {
		t.Fatal("skipped migration should explain why")
	}
}

func TestMigrateCredentialSessionUnsupportedDriverReportsReason(t *testing.T) {
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: t.TempDir()},
		Credentials: []config.CredentialConfig{{
			Name: "dav",
			Type: "webdav",
			Params: config.ParamMap{
				"url":      "https://dav.example",
				"username": "user",
				"password": "secret",
			},
		}},
		Mounts: []config.MountConfig{{
			Name: "dav", Type: "webdav", Credential: "dav",
			Params: config.ParamMap{"root_path": "/"},
		}},
	}
	layout := NewStorageLayout(cfg, RuntimeLayout{})
	result, err := MigrateCredentialSession(cfg, layout, "dav", "dav", false)
	if err != nil {
		t.Fatalf("migrate on an unsupported driver should not error: %v", err)
	}
	if result.Adopted {
		t.Fatalf("result = %+v, want no adoption", result)
	}
	if result.Reason == "" {
		t.Fatal("unsupported migration should explain why")
	}
}
