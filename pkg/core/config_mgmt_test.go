package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/config"
	"github.com/yinzhenyu/qrypt/pkg/core/internal/credentials"
	"github.com/yinzhenyu/qrypt/pkg/util"
)

// TestApplyConfigUpdateKeepsSecretParams guards against the settings-UI round
// trip erasing credentials: summary masks secrets with "***", and an update
// that echoes the placeholder back must keep the previous value.
func TestApplyConfigUpdateKeepsSecretParams(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "web"
type = "webdav"
[mounts.params]
url = "https://dav.example.com"
username = "alice"
password = "real-secret"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{configPath: configPath}

	raw, err := c.ConfigSummaryJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"password":"***"`) {
		t.Fatalf("summary = %s, want password masked", raw)
	}
	if strings.Contains(raw, "real-secret") {
		t.Fatalf("summary = %s, must not leak the secret", raw)
	}

	// Edit the plain url field, echo the masked placeholder for password.
	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Mounts: []ConfigMountUpdate{{
		Action: "update",
		Name:   "web",
		Type:   "webdav",
		Params: map[string]string{"url": "https://dav2.example.com", "username": "alice", "password": "***"},
	}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `password = "real-secret"`) {
		t.Fatalf("password clobbered by masked placeholder:\n%s", content)
	}
	if !strings.Contains(content, "dav2.example.com") {
		t.Fatalf("plain param update not applied:\n%s", content)
	}

	// A real new value still overwrites the secret.
	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Mounts: []ConfigMountUpdate{{
		Action: "update",
		Name:   "web",
		Type:   "webdav",
		Params: map[string]string{"password": "new-secret"},
	}}}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `password = "new-secret"`) {
		t.Fatalf("real secret update not applied:\n%s", data)
	}
}

// TestApplyConfigUpdateFieldsMerge ensures an update that mentions only some
// params keeps the others (field-level patch semantics).
func TestApplyConfigUpdateSharedCredential(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[credentials]]
name = "main"
type = "quark"
[credentials.params]
cookie = "real-cookie"

[[mounts]]
name = "a"
type = "quark"
credential = "main"
[mounts.params]
root_path = "/A"

[[mounts]]
name = "b"
type = "quark"
credential = "main"
[mounts.params]
root_path = "/B"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Core{configPath: configPath}

	summary, err := c.configSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Credentials) != 1 {
		t.Fatalf("credentials = %+v, want one entry", summary.Credentials)
	}
	cred := summary.Credentials[0]
	if cred.Name != "main" || cred.Type != "quark" {
		t.Fatalf("credential summary = %+v", cred)
	}
	if cred.Params["cookie"] != maskedSecretValue {
		t.Fatalf("cookie = %q, want masked", cred.Params["cookie"])
	}
	if strings.Join(cred.UsedBy, ",") != "a,b" {
		t.Fatalf("used_by = %v, want both mounts", cred.UsedBy)
	}
	for _, mount := range summary.Mounts {
		if mount.Credential != "main" {
			t.Fatalf("mount %q credential = %q, want main", mount.Name, mount.Credential)
		}
	}

	// Echoing the mask keeps the stored secret, while a real value replaces it.
	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Credentials: []ConfigCredentialUpdate{{
		Action: "update",
		Name:   "main",
		Type:   "quark",
		Params: map[string]string{"cookie": maskedSecretValue},
	}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `cookie = "real-cookie"`) {
		t.Fatalf("masked echo clobbered the credential:\n%s", data)
	}

	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Credentials: []ConfigCredentialUpdate{{
		Action: "update",
		Name:   "main",
		Type:   "quark",
		Params: map[string]string{"cookie": "rotated-cookie"},
	}}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	params, err := cfg.ResolveMountParams(cfg.Mounts[1])
	if err != nil {
		t.Fatal(err)
	}
	if params["cookie"] != "rotated-cookie" || params["root_path"] != "/B" {
		t.Fatalf("effective params = %+v, want updated credential and mount path", params)
	}

	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Credentials: []ConfigCredentialUpdate{{
		Action: "remove", Name: "main",
	}}}); err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("removing a referenced credential error = %v, want a reference error", err)
	}
}

func TestApplyConfigUpdateFieldsMerge(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "web"
type = "webdav"
[mounts.params]
url = "https://dav.example.com"
username = "alice"
password = "real-secret"
root_path = "/keep"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{configPath: configPath}
	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Mounts: []ConfigMountUpdate{{
		Action: "update",
		Name:   "web",
		Type:   "webdav",
		Params: map[string]string{"url": "https://new.example.com"},
	}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{`password = "real-secret"`, `username = "alice"`, `root_path = "/keep"`, "new.example.com"} {
		if !strings.Contains(content, want) {
			t.Fatalf("merged config missing %q:\n%s", want, content)
		}
	}
}

// TestApplyConfigUpdateUploadFieldMerge ensures a partial upload update only
// touches the provided fields instead of zeroing the rest.
func TestApplyConfigUpdateUploadFieldMerge(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
[[mounts]]
name = "quark"
type = "localfs"
[mounts.params]
root_path = "/tmp"

[upload]
upload_delay = "30s"
upload_workers = 4
delete_delay = "1m"
default_mount = "quark"
default_path = "/backup"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Core{configPath: configPath}

	delay := "5s"
	if _, err := c.ApplyConfigUpdate(ConfigUpdateRequest{Upload: &UploadSummary{UploadDelay: &delay}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{`upload_workers = 4`, `delete_delay = "1m"`, `default_mount = "quark"`, `default_path = "/backup"`, `upload_delay = "5s"`} {
		if !strings.Contains(content, want) {
			t.Fatalf("partial upload update clobbered %q:\n%s", want, content)
		}
	}
}

// TestCoreReloadClosesOldCore verifies Reload shuts down the previous core's
// resources before handing out the replacement.
func TestCoreReloadClosesOldCore(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte("[[mounts]]\nname = \"loc\"\ntype = \"localfs\"\n[mounts.params]\nroot_path = "+util.TOMLPath(remote)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old, err := Open(ctx, Options{ConfigPath: configPath, Runtime: testRuntimeLayout(tmp)})
	if err != nil {
		t.Fatal(err)
	}
	if old.cleanup == nil {
		t.Fatal("old core should be open")
	}
	reloaded, err := old.Reload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close(ctx)
	if old.cleanup != nil {
		t.Fatal("Reload must close the old core's filesystem resources")
	}
	if old.fs != nil {
		t.Fatal("Reload must detach the old core's filesystem")
	}
}

func TestConfigSummaryCredentialLifecycle(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	cfg := &config.Config{
		Storage: config.StorageConfig{WorkDir: tmp},
		Credentials: []config.CredentialConfig{{
			Name: "main", Type: "quark", Params: config.ParamMap{"cookie": "k=v"},
		}},
		Mounts: []config.MountConfig{{
			Name: "docs", Type: "quark", Credential: "main",
			Params: config.ParamMap{"root_path": "/Docs"},
		}},
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	layout := NewStorageLayout(cfg, RuntimeLayout{})
	session := credentials.Open(layout.StateDir).Session(credentials.Spec{
		Name:       "main",
		ParamsSeed: credentials.ParamsSeed("quark", cfg.Credentials[0].Params),
	})
	if _, err := session.Update(0, []byte(`{"cookie":"rotated"}`), "account-a"); err != nil {
		t.Fatal(err)
	}

	c := &Core{configPath: configPath, runtimeLayout: layout}
	summary, err := c.configSummary()
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Credentials) != 1 {
		t.Fatalf("credentials = %+v, want one entry", summary.Credentials)
	}
	entry := summary.Credentials[0]
	if entry.Source != "state" || entry.Version != 1 || entry.Account != "account-a" || entry.UpdatedAt.IsZero() {
		t.Fatalf("credential lifecycle summary = %+v", entry)
	}
}
