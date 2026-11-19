package crypt

import (
	"encoding/hex"
	"testing"
)

// testArgon2Params are deliberately tiny Argon2id cost parameters. The
// determinism and sensitivity suites only care that the function is a pure map
// from (password, salt) to a fixed-size key, so paying for a production-cost
// derivation would only slow the race build down.
var testArgon2Params = Argon2Params{Time: 1, Memory: 1024, Threads: 1}

func TestDefaultArgon2Params(t *testing.T) {
	t.Parallel()
	// The derived rclone password is an on-disk interoperability contract:
	// changing these values makes existing data unreadable.
	want := Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 4}
	if got := DefaultArgon2Params(); got != want {
		t.Fatalf("DefaultArgon2Params() = %+v, want %+v", got, want)
	}
}

func TestArgon2idStretch_Determinism(t *testing.T) {
	t.Parallel()
	password := "correct horse battery staple"
	salt := "a1b2c3d4e5f6a7b8"

	result1 := argon2idStretchWith(testArgon2Params, password, salt)
	result2 := argon2idStretchWith(testArgon2Params, password, salt)
	result3 := argon2idStretchWith(testArgon2Params, password, salt)

	if result1 != result2 || result2 != result3 {
		t.Fatal("argon2idStretchWith must be deterministic: same inputs produced different outputs")
	}
}

func TestArgon2idStretch_KeySensitivity(t *testing.T) {
	t.Parallel()
	salt := "a1b2c3d4e5f6a7b8"

	a := argon2idStretchWith(testArgon2Params, "password", salt)
	b := argon2idStretchWith(testArgon2Params, "Password", salt)

	if a == b {
		t.Fatal("argon2idStretchWith must be sensitive to password case changes")
	}
}

func TestArgon2idStretch_SaltSensitivity(t *testing.T) {
	t.Parallel()
	password := "correct horse battery staple"

	a := argon2idStretchWith(testArgon2Params, password, "salt-a")
	b := argon2idStretchWith(testArgon2Params, password, "salt-b")

	if a == b {
		t.Fatal("argon2idStretchWith must be sensitive to salt changes")
	}
}

func TestArgon2idStretch_OutputFormat(t *testing.T) {
	t.Parallel()
	result := argon2idStretchWith(testArgon2Params, "password", "salt")

	b, err := hex.DecodeString(result)
	if err != nil {
		t.Fatalf("output must be valid hex: %v", err)
	}
	if len(b) != argon2KeyLen {
		t.Fatalf("expected %d bytes, got %d", argon2KeyLen, len(b))
	}
}

func TestArgon2idStretch_NotEmpty(t *testing.T) {
	t.Parallel()
	result := argon2idStretchWith(testArgon2Params, "password", "salt")
	if result == "" {
		t.Fatal("argon2idStretchWith must not return empty string")
	}
}

// TestArgon2idStretch_ProductionCompatVector pins the production-cost result of
// the exported rclone password path. Unlike the suites above it deliberately
// pays for one full-cost derivation: the hex value is what users must paste into
// rclone, so a change here (cost, salt domain separation, encoding) would
// silently break every existing argon2id mount.
func TestArgon2idStretch_ProductionCompatVector(t *testing.T) {
	t.Parallel()
	const want = "af9b7b3a355ac0e5a759638005cc819d2e932007ffce40713cdbcc9096f3dd14" +
		"a5a6ffd2080a111499d315d0e9b92f7ea8b23fa1eacb09fbab5b28566d04deda"

	got, err := ExportRclonePassword(Config{
		Password:     "pass",
		Salt:         "salt",
		PasswordHash: PasswordHashArgon2id,
	})
	if err != nil {
		t.Fatalf("ExportRclonePassword: %v", err)
	}
	if got != want {
		t.Fatalf("production argon2id vector changed:\n got %s\nwant %s", got, want)
	}
}
