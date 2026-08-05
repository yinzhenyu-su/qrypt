package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMaskLine covers the config-show redaction logic: only keys known to be
// secrets are masked, values survive quoting and embedded '=' characters,
// empty values render as empty strings, and non-key lines pass through.
func TestMaskLine(t *testing.T) {
	secrets := map[string]bool{"password": true, "refresh_token": true}
	tests := []struct {
		name string
		line string
		want string
	}{
		{"plain key unchanged", "name = qrypt", "name = qrypt"},
		{"secret bare value", "password = hunter2", `password = "******"`},
		{"secret quoted value", `refresh_token = "abc"`, `refresh_token = "******"`},
		{"secret empty value", "password =", `password = ""`},
		{"secret empty quoted", `password = ""`, `password = ""`},
		{"secret value with equals", "password = a=b=c", `password = "******"`},
		{"secret key padded", "  password  =  hunter2  ", `password = "******"`},
		{"no equals passthrough", "some bare line", "some bare line"},
		{"inline comment kept", "root_path = /tmp/x # local dir", "root_path = /tmp/x # local dir"},
	}
	for _, tt := range tests {
		if got := maskLine(tt.line, secrets); got != tt.want {
			t.Errorf("%s: maskLine(%q) = %q, want %q", tt.name, tt.line, got, tt.want)
		}
	}
}

// TestMaskHidesEverything ensures mask never leaks the input (it is the one
// place every secret passes through).
func TestMaskHidesEverything(t *testing.T) {
	if got := mask("super-secret-value"); got != "******" {
		t.Fatalf("mask() = %q, want ******", got)
	}
}

// TestIsSectionHeader covers the TOML section detection used to insert blank
// lines between mount blocks in config show output.
func TestIsSectionHeader(t *testing.T) {
	headers := []string{"[mounts.local]", "[mounts.quark]", "[mounts.\"weird name\"]"}
	for _, h := range headers {
		if !isSectionHeader(h) {
			t.Errorf("isSectionHeader(%q) = false, want true", h)
		}
	}
	nonHeaders := []string{"", "plain", "[unclosed", "closed]", "  [padded]  ", "[mounts.local] extra", "key = [not-a-header]"}
	for _, h := range nonHeaders {
		if isSectionHeader(h) {
			t.Errorf("isSectionHeader(%q) = true, want false", h)
		}
	}
}

// TestConfigShowMasksSecrets is the end-to-end guarantee: running
// `qrypt config show` on a config with driver secrets must never print the
// plaintext, while non-secret parameters stay readable.
func TestConfigShowMasksSecrets(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "qrypt.toml")
	content := `# qrypt config
[[mounts]]
name = "local"
type = "localfs"

[mounts.params]
root_path = "/tmp/nothing-here"

[[mounts]]
name = "quark"
type = "quark"

[mounts.params]
cookie = "top-secret-quark-cookie"
root_path = "/quark"
base_url = "https://example.com"

[[mounts]]
name = "another"
type = "quark"

[mounts.params]
cookie = ""
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newConfigShowCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Flags().Set("config", configPath); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	// The secret plaintext must never appear.
	if strings.Contains(got, "top-secret-quark-cookie") {
		t.Fatalf("config show leaked a secret:\n%s", got)
	}
	// The masked form appears, including the empty-secret case.
	if !strings.Contains(got, `cookie = "******"`) {
		t.Fatalf("config show did not mask cookie:\n%s", got)
	}
	// Non-secret parameters are readable.
	if !strings.Contains(got, `root_path = "/quark"`) {
		t.Fatalf("config show hid a non-secret parameter:\n%s", got)
	}
	if !strings.Contains(got, `base_url = "https://example.com"`) {
		t.Fatalf("config show hid base_url:\n%s", got)
	}
}
