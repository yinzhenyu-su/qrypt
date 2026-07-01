package crypt

import "testing"

func TestRcloneConfigObscureRoundTrip(t *testing.T) {
	obscured, err := ObscureRcloneConfigValue("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	revealed, err := RevealRcloneConfigValue(obscured)
	if err != nil {
		t.Fatal(err)
	}
	if revealed != "secret-password" {
		t.Fatalf("revealed = %q, want secret-password", revealed)
	}
}

func TestNewRcloneCipherFromConfigRevealsObscuredPasswordAndSalt(t *testing.T) {
	password, err := ObscureRcloneConfigValue("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	salt, err := ObscureRcloneConfigValue("secret-salt")
	if err != nil {
		t.Fatal(err)
	}

	fromPlain, err := NewRcloneCipherFromConfig(Config{Password: "secret-password", Salt: "secret-salt"})
	if err != nil {
		t.Fatal(err)
	}
	fromObscured, err := NewRcloneCipherFromConfig(Config{
		Password:         password,
		PasswordObscured: true,
		Salt:             salt,
		SaltObscured:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	name := "file.txt"
	if fromObscured.EncryptSegment(name) != fromPlain.EncryptSegment(name) {
		t.Fatal("obscured config did not produce the same cipher as plaintext config")
	}
}

func TestRevealRcloneConfigValueRejectsPlaintext(t *testing.T) {
	if _, err := RevealRcloneConfigValue("plain-password"); err == nil {
		t.Fatal("expected reveal to reject plaintext")
	}
}
