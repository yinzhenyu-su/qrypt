package crypt

import (
	"sync"
	"testing"
)

// The tests in this file deliberately do not call t.Parallel: they mutate the
// process-wide test-only Argon2 cost override, so they must not overlap other
// tests that assert the production default (notably
// TestArgon2idStretch_ProductionCompatVector). Sequential tests all finish
// before top-level parallel tests resume, and every test restores the override.

// TestSetArgon2ParamsForTestOverrideAndRestore pins the core contract: the
// override changes what DefaultArgon2Params reports, and the returned function
// restores the previous value.
func TestSetArgon2ParamsForTestOverrideAndRestore(t *testing.T) {
	original := DefaultArgon2Params()
	tiny := testArgon2Params

	restore := SetArgon2ParamsForTest(tiny)
	defer restore()

	if got := DefaultArgon2Params(); got != tiny {
		t.Fatalf("DefaultArgon2Params() = %+v, want override %+v", got, tiny)
	}

	restore()
	if got := DefaultArgon2Params(); got != original {
		t.Fatalf("after restore DefaultArgon2Params() = %+v, want production %+v", got, original)
	}
}

// TestSetArgon2ParamsForTestChangesDerivationCost proves the override reaches
// the derivation used by the exported production API: with the override active,
// ExportRclonePassword must produce exactly the tiny-cost key, and the cipher
// built through NewRcloneCipherFromConfig must still round-trip a path the way a
// cli-style encrypted mount does.
func TestSetArgon2ParamsForTestChangesDerivationCost(t *testing.T) {
	cfg := Config{
		Password:     "pass",
		Salt:         "salt",
		PasswordHash: PasswordHashArgon2id,
	}
	want, err := exportRclonePasswordWithParams(cfg, testArgon2Params)
	if err != nil {
		t.Fatalf("exportRclonePasswordWithParams: %v", err)
	}

	restore := SetArgon2ParamsForTest(testArgon2Params)
	defer restore()

	got, err := ExportRclonePassword(cfg)
	if err != nil {
		t.Fatalf("ExportRclonePassword: %v", err)
	}
	if got != want {
		t.Fatalf("ExportRclonePassword under override = %s, want tiny-cost key %s", got, want)
	}

	// A cli-style encrypted mount path: build the cipher through the production
	// constructor and encrypt/decrypt a file name with it.
	cp, err := NewRcloneCipherFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewRcloneCipherFromConfig: %v", err)
	}
	encrypted := cp.EncryptSegment("hello.txt")
	decrypted, err := cp.DecryptSegment(encrypted)
	if err != nil {
		t.Fatalf("DecryptSegment: %v", err)
	}
	if decrypted != "hello.txt" {
		t.Fatalf("encrypted mount round-trip = %q, want hello.txt", decrypted)
	}
}

// TestSetArgon2ParamsForTestRaceFree exercises concurrent readers while the
// override is installed and restored. Its value is the -race run: reads must go
// through the atomic pointer, not a plain package variable.
func TestSetArgon2ParamsForTestRaceFree(t *testing.T) {
	restore := SetArgon2ParamsForTest(testArgon2Params)
	defer restore()

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			params := DefaultArgon2Params()
			if params != testArgon2Params {
				t.Errorf("DefaultArgon2Params() = %+v, want override", params)
				return
			}
			argon2idStretchWith(params, "password", "salt")
		}()
	}
	wg.Wait()
}

// TestSetArgon2ParamsForTestRejectsInvalidCost makes sure a bad override fails
// loudly in the helper instead of panicking deep inside Argon2 or silently
// deriving with a weakened cost.
func TestSetArgon2ParamsForTestRejectsInvalidCost(t *testing.T) {
	original := DefaultArgon2Params()
	for _, params := range []Argon2Params{
		{Time: 0, Memory: 1024, Threads: 1},
		{Time: 1, Memory: 0, Threads: 1},
		{Time: 1, Memory: 1024, Threads: 0},
		{Time: 1, Memory: 7, Threads: 1},
		{Time: 1, Memory: 8, Threads: 2},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("SetArgon2ParamsForTest(%+v) did not panic", params)
				}
			}()
			SetArgon2ParamsForTest(params)
		}()
	}
	if got := DefaultArgon2Params(); got != original {
		t.Fatalf("rejected overrides changed DefaultArgon2Params to %+v, want %+v", got, original)
	}
}
