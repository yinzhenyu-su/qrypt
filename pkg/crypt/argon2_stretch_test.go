package crypt

import (
	"encoding/hex"
	"testing"
)

func TestArgon2idStretch_Determinism(t *testing.T) {
	password := "correct horse battery staple"
	salt := "a1b2c3d4e5f6a7b8"

	result1 := argon2idStretch(password, salt)
	result2 := argon2idStretch(password, salt)
	result3 := argon2idStretch(password, salt)

	if result1 != result2 || result2 != result3 {
		t.Fatal("argon2idStretch must be deterministic: same inputs produced different outputs")
	}
}

func TestArgon2idStretch_KeySensitivity(t *testing.T) {
	salt := "a1b2c3d4e5f6a7b8"

	a := argon2idStretch("password", salt)
	b := argon2idStretch("Password", salt)

	if a == b {
		t.Fatal("argon2idStretch must be sensitive to password case changes")
	}
}

func TestArgon2idStretch_SaltSensitivity(t *testing.T) {
	password := "correct horse battery staple"

	a := argon2idStretch(password, "salt-a")
	b := argon2idStretch(password, "salt-b")

	if a == b {
		t.Fatal("argon2idStretch must be sensitive to salt changes")
	}
}

func TestArgon2idStretch_OutputFormat(t *testing.T) {
	result := argon2idStretch("password", "salt")

	b, err := hex.DecodeString(result)
	if err != nil {
		t.Fatalf("output must be valid hex: %v", err)
	}
	if len(b) != argon2KeyLen {
		t.Fatalf("expected %d bytes, got %d", argon2KeyLen, len(b))
	}
}

func TestArgon2idStretch_NotEmpty(t *testing.T) {
	result := argon2idStretch("password", "salt")
	if result == "" {
		t.Fatal("argon2idStretch must not return empty string")
	}
}
