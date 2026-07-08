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

func TestConfigRejectsUnsupportedPasswordHash(t *testing.T) {
	err := Config{Password: "pass", PasswordHash: "sha256"}.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported password_hash")
	}
}

func TestConfigArgon2idValidate(t *testing.T) {
	err := Config{Password: "pass", PasswordHash: PasswordHashArgon2id}.Validate()
	if err != nil {
		t.Fatal(err)
	}
}

func TestConfigEmptyPasswordHashValidate(t *testing.T) {
	err := Config{Password: "pass", PasswordHash: ""}.Validate()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGenerateSalt(t *testing.T) {
	s1, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	if s1 == "" || s2 == "" {
		t.Fatal("GenerateSalt must not return empty string")
	}
	if len(s1) != 32 {
		t.Fatalf("expected 32-char hex salt, got %d", len(s1))
	}
	if s1 == s2 {
		t.Fatal("two successive GenerateSalt calls must return different values")
	}
}

func TestNewRcloneCipherFromConfigBackwardCompat(t *testing.T) {
	cp, err := NewRcloneCipherFromConfig(Config{
		Password:         "pass",
		Salt:             "salt",
		PasswordHash:     "",
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := cp.EncryptSegment("hello.txt")
	dec, err := cp.DecryptSegment(enc)
	if err != nil || dec != "hello.txt" {
		t.Fatalf("backward compat (no password_hash): roundtrip failed: %v", err)
	}
}

func TestNewRcloneCipherFromConfigArgon2id(t *testing.T) {
	cp, err := NewRcloneCipherFromConfig(Config{
		Password:         "pass",
		Salt:             "salt",
		PasswordHash:     PasswordHashArgon2id,
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := cp.EncryptSegment("hello.txt")
	dec, err := cp.DecryptSegment(enc)
	if err != nil || dec != "hello.txt" {
		t.Fatalf("argon2id mode: roundtrip failed: %v", err)
	}
}

func TestNewRcloneCipherFromConfigArgon2idDifferentKey(t *testing.T) {
	noHash, _ := NewRcloneCipherFromConfig(Config{
		Password:     "pass",
		Salt:         "salt",
		PasswordHash: "",
	})
	withHash, _ := NewRcloneCipherFromConfig(Config{
		Password:     "pass",
		Salt:         "salt",
		PasswordHash: PasswordHashArgon2id,
	})

	enc1 := noHash.EncryptSegment("hello.txt")
	enc2 := withHash.EncryptSegment("hello.txt")

	if enc1 == enc2 {
		t.Fatal("argon2id must produce different ciphertext than plain scrypt")
	}
}

func TestExportRclonePassword_NoHash(t *testing.T) {
	pw, err := ExportRclonePassword(Config{
		Password:     "my-password",
		Salt:         "salt",
		PasswordHash: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pw != "my-password" {
		t.Fatalf("without password_hash, password must be unchanged: got %q", pw)
	}
}

func TestExportRclonePassword_Argon2id(t *testing.T) {
	pw, err := ExportRclonePassword(Config{
		Password:     "my-password",
		Salt:         "salt",
		PasswordHash: PasswordHashArgon2id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pw == "my-password" {
		t.Fatal("with argon2id, password must differ from raw input")
	}
	if len(pw) != 128 {
		t.Fatalf("argon2id output must be 64-byte hex (128 chars), got %d", len(pw))
	}
}

func TestExportRclonePassword_Argon2idWithObscured(t *testing.T) {
	obscured, err := ObscureRcloneConfigValue("my-password")
	if err != nil {
		t.Fatal(err)
	}
	pw, err := ExportRclonePassword(Config{
		Password:         obscured,
		Salt:             "salt",
		PasswordHash:     PasswordHashArgon2id,
		PasswordObscured: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pw == obscured {
		t.Fatal("exported password must reveal obscured value")
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
