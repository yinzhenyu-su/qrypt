package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/crypt"
)

func TestEncryptionForSupportsLegacyShapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	err := os.WriteFile(path, []byte(`
[defaults.encryption]
password = "default-pass"
salt = "default-salt"
filename_encryption = "standard"
filename_encoding = "base32"

[[mounts]]
name = "work"
[mounts.encryption]
password = "mount-pass"
salt = "mount-salt"
filename_encryption = "obfuscate"
filename_encoding = "base64"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := cfg.EncryptionFor("work")
	if enc.Password != "mount-pass" {
		t.Fatalf("expected mount password, got %q", enc.Password)
	}
	if enc.Salt != "mount-salt" {
		t.Fatalf("expected mount salt, got %q", enc.Salt)
	}
	if enc.FileNameEncryption != crypt.FileNameEncryptionObfuscate {
		t.Fatalf("expected obfuscate, got %q", enc.FileNameEncryption)
	}
	if enc.FileNameEncoding != crypt.FileNameEncodingBase64 {
		t.Fatalf("expected base64, got %q", enc.FileNameEncoding)
	}
}

func TestApplyEncryptionOverrides(t *testing.T) {
	enc := crypt.Config{
		Password:           "config-pass",
		Salt:               "config-salt",
		FileNameEncryption: crypt.FileNameEncryptionStandard,
		FileNameEncoding:   crypt.FileNameEncodingBase32,
	}
	password := "cli-pass"
	salt := ""
	mode := crypt.FileNameEncryptionOff
	enc = ApplyEncryptionOverrides(enc, EncryptionOverrides{
		Password:           &password,
		Salt:               &salt,
		FileNameEncryption: &mode,
	})
	if enc.Password != "cli-pass" {
		t.Fatalf("expected CLI password, got %q", enc.Password)
	}
	if enc.Salt != "" {
		t.Fatalf("expected CLI empty salt, got %q", enc.Salt)
	}
	if enc.FileNameEncryption != crypt.FileNameEncryptionOff {
		t.Fatalf("expected off, got %q", enc.FileNameEncryption)
	}
	if enc.FileNameEncoding != crypt.FileNameEncodingBase32 {
		t.Fatalf("expected inherited base32, got %q", enc.FileNameEncoding)
	}
}

func TestEffectiveMountPointPrefersTopLevel(t *testing.T) {
	cfg := &Config{
		MountPoint: "~/Qrypt",
		Mounts: []MountConfig{
			{Name: "legacy", MountPoint: "~/Legacy"},
		},
	}
	if got := cfg.EffectiveMountPoint(); got != "~/Qrypt" {
		t.Fatalf("expected top-level mount point, got %q", got)
	}
}

func TestEffectiveMountPointFallsBackToLegacyMountField(t *testing.T) {
	cfg := &Config{
		Mounts: []MountConfig{
			{Name: "legacy", MountPoint: "~/Legacy"},
		},
	}
	if got := cfg.EffectiveMountPoint(); got != "~/Legacy" {
		t.Fatalf("expected legacy mount point fallback, got %q", got)
	}
}

func TestLoadLoggingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	err := os.WriteFile(path, []byte(`
volume_name = "Qrypt Test"
no_apple_double = false

[logging]
fuse_trace = true
fuse_trace_file = "/tmp/qrypt-fuse.log"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Logging.FuseTrace {
		t.Fatal("expected fuse_trace to be enabled")
	}
	if cfg.Logging.FuseTraceFile != "/tmp/qrypt-fuse.log" {
		t.Fatalf("unexpected fuse_trace_file: %q", cfg.Logging.FuseTraceFile)
	}
	if cfg.EffectiveVolumeName() != "Qrypt Test" {
		t.Fatalf("unexpected volume_name: %q", cfg.EffectiveVolumeName())
	}
	if cfg.EffectiveNoAppleDouble() {
		t.Fatal("expected no_apple_double to be disabled")
	}
}

func TestMountOptionsDefaults(t *testing.T) {
	cfg := &Config{}
	if cfg.EffectiveVolumeName() != "Qrypt" {
		t.Fatalf("unexpected default volume name: %q", cfg.EffectiveVolumeName())
	}
	if !cfg.EffectiveNoAppleDouble() {
		t.Fatal("expected no_apple_double to default to true")
	}
}
