package crypt

import (
	"sync/atomic"
)

// ScryptParams are the scrypt KDF cost parameters used to derive the rclone
// data and name keys. The values are part of the on-disk key derivation
// contract shared with rclone: changing them changes every derived key and
// makes existing data unreadable, so production code must keep DefaultScryptParams.
type ScryptParams struct {
	// N is the CPU/memory cost parameter; it must be a power of two greater
	// than one.
	N int
	// R is the block size parameter.
	R int
	// P is the parallelization parameter.
	P int
}

// testScryptParamsOverride holds the process-wide test-only cost override. It
// is nil except while a test has called SetScryptParamsForTest. An atomic
// pointer keeps concurrent cipher constructions race-free without a mutex on
// the derivation path.
var testScryptParamsOverride atomic.Pointer[ScryptParams]

// DefaultScryptParams returns the production scrypt cost used by rclone: N =
// 16384, r = 8, p = 1. These values are the rclone on-disk format contract and
// cannot be tuned without breaking interoperability.
//
// While a test has installed an override with SetScryptParamsForTest, this
// returns the override instead. Production code never installs one, so in
// production this always returns the constants above.
func DefaultScryptParams() ScryptParams {
	if override := testScryptParamsOverride.Load(); override != nil {
		return *override
	}
	return ScryptParams{N: 16384, R: 8, P: 1}
}

// SetScryptParamsForTest overrides DefaultScryptParams for the current test
// binary and returns a function that restores the previous value.
//
// scrypt is much more expensive than Argon2id for the small inputs qrypt uses,
// and every encrypted command builds a fresh cipher from the config. Tests in
// other packages (notably internal/cli) therefore pay hundreds of milliseconds
// per cipher build under -race. Installing a tiny cost from TestMain keeps the
// production derivation path exercised while removing that cost.
//
// The override is process-wide because the production default is read without
// an explicit parameter; reads are atomic, so installing or restoring it
// concurrently with cipher construction is race-free. Tests that install it
// must not run concurrently with tests that assert production-derived key
// bytes (pkg/crypt's rclone compatibility vectors keep the production default
// and live in this package). Install it before the package's parallel tests
// start (TestMain) and call the returned restore function in LIFO order.
//
// The function panics on invalid cost parameters so a misconfigured override
// fails loudly instead of silently weakening or breaking derivations.
//
// This is test-only API: production code must never call it.
func SetScryptParamsForTest(p ScryptParams) (restore func()) {
	if p.N <= 1 || p.N&(p.N-1) != 0 {
		panic("crypt: scrypt N must be a power of two greater than one")
	}
	if p.R <= 0 {
		panic("crypt: scrypt r must be positive")
	}
	if p.P <= 0 {
		panic("crypt: scrypt p must be positive")
	}
	previous := testScryptParamsOverride.Load()
	testScryptParamsOverride.Store(&p)
	return func() {
		testScryptParamsOverride.Store(previous)
	}
}
