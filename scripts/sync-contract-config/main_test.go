package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestGHSecretUsesStdinAndHidesFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell stub; selection tests are portable")
	}
	dir := t.TempDir()
	stub := `#!/bin/sh
if [ "$1" = secret ]; then
  test "$#" = 5 || exit 2
  test "$2" = set || exit 2
  test "$3" = CONTRACT_CONFIG_TOML_B64 || exit 2
  test "$4" = --repo || exit 2
  test "$5" = owner/repo || exit 2
  IFS= read -r value || true
  test "$value" = synthetic-secret || exit 2
  exit 0
fi
echo synthetic-secret >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := gh(strings.NewReader("synthetic-secret"), "secret", "set", "CONTRACT_CONFIG_TOML_B64", "--repo", "owner/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := gh(nil, "fail"); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("command failure missing or sensitive output leaked")
	}
}

const sampleConfig = `version = "1"
[encryption]
password = "global-secret"
[upload]
upload_workers = 3
default_mount = "excluded"
default_path = "/Inbox"
[[mounts]]
name = "excluded"
type = "webdav"
test_enabled = true
[mounts.params]
password = "excluded-secret"
[[mounts]]
name = "selected"
type = "webdav"
test_enabled = true
[mounts.params]
password = "selected-secret"
[mounts.encryption]
password = "encryption-secret"
[[mounts]]
name = "disabled"
type = "webdav"
`

func TestSelectConfig(t *testing.T) {
	data, count, err := selectConfig([]byte(sampleConfig), []byte(`["selected"]`))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || strings.Contains(string(data), "excluded-secret") {
		t.Fatal("unselected mount was not excluded")
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal("output is not valid TOML")
	}
	mounts := cfg["mounts"].([]map[string]any)
	if len(mounts) != 1 || mounts[0]["name"] != "selected" || mounts[0]["test_enabled"] != true {
		t.Fatal("incorrect mount selection")
	}
	for _, secret := range []string{"global-secret", "selected-secret", "encryption-secret"} {
		if !strings.Contains(string(data), secret) {
			t.Fatal("required configuration was lost")
		}
	}
	upload := cfg["upload"].(map[string]any)
	if upload["default_mount"] != nil || upload["default_path"] != nil || upload["upload_workers"] != int64(3) {
		t.Fatal("incorrect handling of excluded upload default")
	}
}

func TestSelectConfigRetainsOrderAndValidDefault(t *testing.T) {
	data, count, err := selectConfig([]byte(sampleConfig), []byte(`["selected","excluded"]`))
	if err != nil || count != 2 {
		t.Fatalf("selection count = %d, error = %v", count, err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	mounts := cfg["mounts"].([]map[string]any)
	if mounts[0]["name"] != "selected" || mounts[1]["name"] != "excluded" || cfg["upload"].(map[string]any)["default_mount"] != "excluded" {
		t.Fatal("order or selected upload default changed")
	}
}

func TestSelectConfigRejectsUnsafeInput(t *testing.T) {
	for _, mounts := range []string{`null`, `[]`, `{}`, `[1]`, `[""]`, `[" "]`, `["selected","selected"]`, `["missing"]`, `["disabled"]`} {
		t.Run(mounts, func(t *testing.T) {
			if _, _, err := selectConfig([]byte(sampleConfig), []byte(mounts)); err == nil {
				t.Fatal("invalid selection accepted")
			}
		})
	}
	for _, config := range []string{`password = "sensitive-unclosed`, `version = "1"`, sampleConfig + "\n[[mounts]]\nname = 'selected'\n"} {
		_, _, err := selectConfig([]byte(config), []byte(`["selected"]`))
		if err == nil || strings.Contains(err.Error(), "sensitive-unclosed") {
			t.Fatal("invalid config accepted or parser leaked input")
		}
	}
}
