package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMountConfigFromConfig(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	err := os.WriteFile(configPath, []byte(`
volume_name = "Qrypt Dev"
read_only = true
allow_other = true
default_permissions = true
no_apple_double = false
no_apple_xattr = true
attr_timeout = "1500ms"
entry_timeout = "2s"
negative_timeout = "250ms"
total_space = "2T"
free_space = "1.5T"

[logging]
log_level = "debug"
log_file = "`+filepath.Join(tmp, "qrypt.log")+`"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	mountConfig, err := mountConfigFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt Dev" {
		t.Fatalf("unexpected volume name: %q", mountConfig.VolumeName)
	}
	if !mountConfig.ReadOnly {
		t.Fatal("expected read_only to be enabled")
	}
	if !mountConfig.AllowOther {
		t.Fatal("expected allow_other to be enabled")
	}
	if !mountConfig.DefaultPermissions {
		t.Fatal("expected default_permissions to be enabled")
	}
	if mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to be disabled")
	}
	if !mountConfig.NoAppleXattr {
		t.Fatal("expected no_apple_xattr to be enabled")
	}
	if mountConfig.AttrTimeout != 1500*time.Millisecond {
		t.Fatalf("unexpected attr timeout: %s", mountConfig.AttrTimeout)
	}
	if mountConfig.EntryTimeout != 2*time.Second {
		t.Fatalf("unexpected entry timeout: %s", mountConfig.EntryTimeout)
	}
	if mountConfig.NegativeTimeout != 250*time.Millisecond {
		t.Fatalf("unexpected negative timeout: %s", mountConfig.NegativeTimeout)
	}
	if mountConfig.TotalSpace != 2<<40 {
		t.Fatalf("unexpected total space: %d", mountConfig.TotalSpace)
	}
	if mountConfig.FreeSpace != 1536<<30 {
		t.Fatalf("unexpected free space: %d", mountConfig.FreeSpace)
	}
	if mountConfig.Logging.LogLevel != "debug" {
		t.Fatalf("unexpected log_level: %q", mountConfig.Logging.LogLevel)
	}
	if mountConfig.Logging.LogFile != filepath.Join(tmp, "qrypt.log") {
		t.Fatalf("unexpected log_file: %q", mountConfig.Logging.LogFile)
	}
}

func TestMountConfigFromConfigDefaults(t *testing.T) {
	mountConfig, err := mountConfigFromConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.VolumeName != "Qrypt" {
		t.Fatalf("unexpected default volume name: %q", mountConfig.VolumeName)
	}
	if !mountConfig.NoAppleDouble {
		t.Fatal("expected no_apple_double to default to true")
	}
	if mountConfig.NoAppleXattr {
		t.Fatal("expected no_apple_xattr to default to false")
	}
	if mountConfig.ReadOnly {
		t.Fatal("expected read_only to default to false")
	}
	if mountConfig.AllowOther {
		t.Fatal("expected allow_other to default to false")
	}
	if mountConfig.DefaultPermissions {
		t.Fatal("expected default_permissions to default to false")
	}
	if mountConfig.AttrTimeout != time.Second {
		t.Fatalf("unexpected default attr timeout: %s", mountConfig.AttrTimeout)
	}
	if mountConfig.EntryTimeout != time.Second {
		t.Fatalf("unexpected default entry timeout: %s", mountConfig.EntryTimeout)
	}
	if mountConfig.NegativeTimeout != 0 {
		t.Fatalf("unexpected default negative timeout: %s", mountConfig.NegativeTimeout)
	}
}

func TestMountConfigRejectsInvalidMetadataTimeout(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`attr_timeout = "-1s"`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := mountConfigFromConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "attr_timeout") {
		t.Fatalf("expected attr_timeout error, got %v", err)
	}
}

func TestMountConfigAllowsDisablingMetadataTimeouts(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "qrypt.toml")
	if err := os.WriteFile(configPath, []byte(`
attr_timeout = "0s"
entry_timeout = "0s"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	mountConfig, err := mountConfigFromConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mountConfig.AttrTimeout != 0 || !mountConfig.AttrTimeoutSet {
		t.Fatalf("unexpected attr timeout: %s set=%t", mountConfig.AttrTimeout, mountConfig.AttrTimeoutSet)
	}
	if mountConfig.EntryTimeout != 0 || !mountConfig.EntryTimeoutSet {
		t.Fatalf("unexpected entry timeout: %s set=%t", mountConfig.EntryTimeout, mountConfig.EntryTimeoutSet)
	}
}
