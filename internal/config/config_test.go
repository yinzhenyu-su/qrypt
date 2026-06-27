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
read_only = true
allow_other = true
default_permissions = true
no_apple_double = false
no_apple_xattr = true
total_space = "1T"
free_space = "800G"

[logging]
log_level = "debug"
log_file = "/tmp/qrypt.log"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.LogLevel != "debug" {
		t.Fatalf("unexpected log_level: %q", cfg.Logging.LogLevel)
	}
	if cfg.Logging.LogFile != "/tmp/qrypt.log" {
		t.Fatalf("unexpected log_file: %q", cfg.Logging.LogFile)
	}
	if cfg.EffectiveVolumeName() != "Qrypt Test" {
		t.Fatalf("unexpected volume_name: %q", cfg.EffectiveVolumeName())
	}
	if !cfg.ReadOnly {
		t.Fatal("expected read_only to be enabled")
	}
	if !cfg.AllowOther {
		t.Fatal("expected allow_other to be enabled")
	}
	if !cfg.DefaultPermissions {
		t.Fatal("expected default_permissions to be enabled")
	}
	if cfg.EffectiveNoAppleDouble() {
		t.Fatal("expected no_apple_double to be disabled")
	}
	if !cfg.EffectiveNoAppleXattr() {
		t.Fatal("expected no_apple_xattr to be enabled")
	}
	total, free, err := cfg.EffectiveSpaceBytes()
	if err != nil {
		t.Fatal(err)
	}
	if total != 1<<40 {
		t.Fatalf("unexpected total_space: %d", total)
	}
	if free != 800*(1<<30) {
		t.Fatalf("unexpected free_space: %d", free)
	}
}

func TestEffectiveBandwidthLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	err := os.WriteFile(path, []byte(`
[bandwidth]
download = "10M"
upload = "2.5M"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := cfg.EffectiveBandwidthLimits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.DownloadBytesPerSecond != 10*(1<<20) {
		t.Fatalf("download limit = %d, want %d", limits.DownloadBytesPerSecond, 10*(1<<20))
	}
	if limits.UploadBytesPerSecond != int64(2.5*float64(1<<20)) {
		t.Fatalf("upload limit = %d, want %d", limits.UploadBytesPerSecond, int64(2.5*float64(1<<20)))
	}
}

func TestEffectiveBandwidthLimitsRejectsInvalidValue(t *testing.T) {
	cfg := &Config{Bandwidth: BandwidthConfig{Download: "fast"}}
	if _, err := cfg.EffectiveBandwidthLimits(); err == nil {
		t.Fatal("expected invalid download bandwidth to fail")
	}
}

func TestCacheForIncludesOperationDelays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrypt.toml")
	err := os.WriteFile(path, []byte(`
[defaults.cache]
upload_delay = "5s"
upload_workers = 2
delete_delay = "2s"

[[mounts]]
name = "fast"
[mounts.cache]
upload_delay = "250ms"
upload_workers = 8
delete_delay = "500ms"
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.CacheFor("slow").UploadDelay; got != "5s" {
		t.Fatalf("default upload_delay = %q, want 5s", got)
	}
	if got := cfg.CacheFor("slow").UploadWorkers; got != 2 {
		t.Fatalf("default upload_workers = %d, want 2", got)
	}
	if got := cfg.CacheFor("slow").DeleteDelay; got != "2s" {
		t.Fatalf("default delete_delay = %q, want 2s", got)
	}
	if got := cfg.CacheFor("fast").UploadDelay; got != "250ms" {
		t.Fatalf("mount upload_delay = %q, want 250ms", got)
	}
	if got := cfg.CacheFor("fast").UploadWorkers; got != 8 {
		t.Fatalf("mount upload_workers = %d, want 8", got)
	}
	if got := cfg.CacheFor("fast").DeleteDelay; got != "500ms" {
		t.Fatalf("mount delete_delay = %q, want 500ms", got)
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
	if cfg.EffectiveNoAppleXattr() {
		t.Fatal("expected no_apple_xattr to default to false")
	}
	if cfg.ReadOnly {
		t.Fatal("expected read_only to default to false")
	}
	if cfg.AllowOther {
		t.Fatal("expected allow_other to default to false")
	}
	if cfg.DefaultPermissions {
		t.Fatal("expected default_permissions to default to false")
	}
}

func TestParseSize(t *testing.T) {
	tests := map[string]int64{
		"":      0,
		"1024":  1024,
		"1K":    1 << 10,
		"1M":    1 << 20,
		"1G":    1 << 30,
		"1T":    1 << 40,
		"1P":    1 << 50,
		"1GB":   1 << 30,
		"1.5G":  1536 << 20,
		" 2 g ": 2 << 30,
	}
	for value, want := range tests {
		got, err := ParseSize(value)
		if err != nil {
			t.Fatalf("ParseSize(%q) returned error: %v", value, err)
		}
		if got != want {
			t.Fatalf("ParseSize(%q) = %d, want %d", value, got, want)
		}
	}
}

func TestParseSizeRejectsInvalidValue(t *testing.T) {
	if _, err := ParseSize("ten G"); err == nil {
		t.Fatal("expected invalid size to fail")
	}
}
