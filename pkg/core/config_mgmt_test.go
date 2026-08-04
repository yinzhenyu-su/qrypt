package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyConfigUpdateKeepsSecretParams guards against the settings-UI round
// trip erasing credentials: summary masks secrets with "***", and an update
// that echoes the placeholder back must keep the previous value.
func TestApplyConfigUpdateKeepsSecretParams(t *testing.T) {
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
func TestApplyConfigUpdateFieldsMerge(t *testing.T) {
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
