package crypt

import "testing"

// TestDefaultScryptParams pins the rclone on-disk contract: changing these
// values silently changes every derived key and breaks existing data.
func TestDefaultScryptParams(t *testing.T) {
	got := DefaultScryptParams()
	want := ScryptParams{N: 16384, R: 8, P: 1}
	if got != want {
		t.Fatalf("DefaultScryptParams() = %+v, want %+v", got, want)
	}
}

// TestSetScryptParamsForTestOverrideAndRestore pins the override contract
// without t.Parallel: the override is process-wide, so it must not overlap
// tests that assert the production default.
func TestSetScryptParamsForTestOverrideAndRestore(t *testing.T) {
	original := DefaultScryptParams()
	tiny := ScryptParams{N: 2, R: 1, P: 1}

	restore := SetScryptParamsForTest(tiny)
	defer restore()

	if got := DefaultScryptParams(); got != tiny {
		t.Fatalf("DefaultScryptParams() = %+v, want override %+v", got, tiny)
	}
	restore()
	if got := DefaultScryptParams(); got != original {
		t.Fatalf("after restore DefaultScryptParams() = %+v, want production %+v", got, original)
	}
}

// TestSetScryptParamsForTestChangesDerivedKeys proves the override reaches the
// derivation behind the exported API: the same password and salt must derive
// different data keys under different costs, and the cipher must still round
// trip a filename the way an encrypted mount does.
func TestSetScryptParamsForTestChangesDerivedKeys(t *testing.T) {
	const password, salt = "secret", "mysalt"

	production, err := NewRcloneCipher(password, salt)
	if err != nil {
		t.Fatalf("NewRcloneCipher: %v", err)
	}

	restore := SetScryptParamsForTest(ScryptParams{N: 2, R: 1, P: 1})
	tiny, err := NewRcloneCipher(password, salt)
	restore()
	if err != nil {
		t.Fatalf("NewRcloneCipher with override: %v", err)
	}
	if production.dataKey == tiny.dataKey {
		t.Fatal("scrypt cost override did not change the derived data key")
	}

	encrypted := tiny.EncryptSegment("hello.txt")
	decrypted, err := tiny.DecryptSegment(encrypted)
	if err != nil {
		t.Fatalf("DecryptSegment: %v", err)
	}
	if decrypted != "hello.txt" {
		t.Fatalf("round trip = %q, want hello.txt", decrypted)
	}
}

// TestSetScryptParamsForTestRejectsInvalidCost makes a misconfigured override
// fail loudly instead of silently producing a broken derivation.
func TestSetScryptParamsForTestRejectsInvalidCost(t *testing.T) {
	cases := []struct {
		name string
		p    ScryptParams
	}{
		{"non-power-of-two N", ScryptParams{N: 3, R: 8, P: 1}},
		{"N too small", ScryptParams{N: 1, R: 8, P: 1}},
		{"zero R", ScryptParams{N: 2, R: 0, P: 1}},
		{"zero P", ScryptParams{N: 2, R: 1, P: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic for invalid scrypt cost")
				}
				if got := DefaultScryptParams(); got != (ScryptParams{N: 16384, R: 8, P: 1}) {
					t.Fatalf("invalid override mutated the default: %+v", got)
				}
			}()
			SetScryptParamsForTest(tc.p)
		})
	}
}
