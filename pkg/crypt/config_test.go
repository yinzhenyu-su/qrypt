package crypt

import "testing"

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg := Config{Password: "pass"}.WithDefaults()
	if cfg.FileNameEncryption != FileNameEncryptionStandard {
		t.Fatalf("expected standard, got %q", cfg.FileNameEncryption)
	}
	if cfg.FileNameEncoding != FileNameEncodingBase32 {
		t.Fatalf("expected base32, got %q", cfg.FileNameEncoding)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRejectsUnsupportedMode(t *testing.T) {
	err := Config{Password: "pass", FileNameEncryption: "bad", FileNameEncoding: "base32"}.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNewRcloneCipherFromConfigUsesFilenameOptions(t *testing.T) {
	cp, err := NewRcloneCipherFromConfig(Config{
		Password:           "pass",
		Salt:               "salt",
		FileNameEncryption: FileNameEncryptionOff,
		FileNameEncoding:   FileNameEncodingBase64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cp.EncryptSegment("plain.txt"); got != "plain.txt" {
		t.Fatalf("filename encryption off should preserve names, got %q", got)
	}
}
