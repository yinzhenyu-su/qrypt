package crypt

import (
	"encoding/hex"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

const (
	// argon2Time is the default number of iterations for Argon2id.
	// 3 iterations balances security (resist GPU/ASIC parallelism) with
	// interactive latency (~100ms on modern hardware).
	argon2Time = 3

	// argon2Memory is the default Argon2id memory cost in KiB (64 MiB) — high
	// enough to make GPU cracking impractical (RTX 4090 has 24 GiB, so with
	// 64 MiB per hash <=384 concurrent attempts), low enough to not block
	// startup.
	argon2Memory = 64 * 1024

	// argon2Threads is the default Argon2id parallelism. 4 threads is a
	// reasonable default for both desktop and server CPUs.
	argon2Threads = 4

	// argon2KeyLen is the output length in bytes (64 bytes = 128 hex chars).
	// rclone's scrypt expects an alphanumeric password string, so the hex
	// encoding is deliberately long to avoid truncation.
	argon2KeyLen = 64
)

// Argon2Params is the cost configuration of the Argon2id pre-stretch.
//
// Production callers always derive with DefaultArgon2Params; the type exists so
// tests can exercise the same code path with tiny, deterministic cost without
// mutating shared state (the package's tests run in parallel).
type Argon2Params struct {
	// Time is the number of passes over the memory.
	Time uint32
	// Memory is the memory cost in KiB.
	Memory uint32
	// Threads is the parallelism degree.
	Threads uint8
}

// testArgon2ParamsOverride holds the process-wide test-only cost override. It
// is nil except while a test has called SetArgon2ParamsForTest. An atomic
// pointer keeps concurrent derivations (for example from t.Parallel tests that
// read DefaultArgon2Params) race-free without a mutex on the derivation path.
var testArgon2ParamsOverride atomic.Pointer[Argon2Params]

// DefaultArgon2Params returns the production Argon2id cost parameters. These
// values are part of the on-disk key derivation contract: changing them changes
// the exported rclone password and makes existing data unreadable, so keep them
// stable unless a migration is planned.
//
// While a test has installed an override with SetArgon2ParamsForTest, this
// returns the override instead. Production code never installs one, so in
// production this always returns the constants above.
func DefaultArgon2Params() Argon2Params {
	if override := testArgon2ParamsOverride.Load(); override != nil {
		return *override
	}
	return Argon2Params{
		Time:    argon2Time,
		Memory:  argon2Memory,
		Threads: argon2Threads,
	}
}

// SetArgon2ParamsForTest overrides DefaultArgon2Params for the current test
// binary and returns a function that restores the previous value.
//
// It exists so tests in other packages can drive production encryption APIs
// (NewRcloneCipherFromConfig, ExportRclonePassword, ...) with tiny Argon2id cost
// instead of paying for a production-cost derivation on every test. A typical
// caller installs it once from TestMain:
//
//	func TestMain(m *testing.M) {
//		restore := crypt.SetArgon2ParamsForTest(testknobs.FastArgon2)
//		code := m.Run()
//		restore()
//		os.Exit(code)
//	}
//
// The override is process-wide because the production default is read without
// an explicit parameter; it is deliberately not per-goroutine. Reads are atomic,
// so installing or restoring it concurrently with derivations is race-free, but
// tests that install it must not run concurrently with tests that assert the
// production cost. In particular, install the override before the package's
// t.Parallel tests start (TestMain) and call the returned restore function in
// LIFO order.
//
// The function panics on obviously invalid cost parameters (Argon2 requires
// at least 8 KiB of memory per thread) so a misconfigured override fails loudly
// instead of silently weakening or breaking derivations.
//
// This is test-only API: production code must never call it.
func SetArgon2ParamsForTest(p Argon2Params) (restore func()) {
	// argon2.IDKey requires time >= 1, threads >= 1, and memory >= 8*threads.
	// Reject the other obvious misconfiguration (zero memory) too.
	if p.Time == 0 || p.Threads == 0 || p.Memory == 0 || p.Memory < uint32(p.Threads)*8 {
		panic("crypt: SetArgon2ParamsForTest called with invalid Argon2 cost parameters")
	}
	previous := testArgon2ParamsOverride.Swap(&p)
	return func() {
		testArgon2ParamsOverride.Store(previous)
	}
}

// argon2idStretchWith applies Argon2id as a pre-stretch before scrypt.
//
// Same (password, salt) always produces the same output — this is a
// deterministic key derivation function, not a random source.
//
// The salt is domain-separated with "qrypt-argon2id:" to prevent
// accidental reuse with other KDF contexts.
//
// The function takes explicit cost parameters because it must be reachable from
// tests with tiny cost; production callers pass DefaultArgon2Params.
func argon2idStretchWith(params Argon2Params, password, salt string) string {
	saltBytes := []byte("qrypt-argon2id:" + salt)
	key := argon2.IDKey(
		[]byte(password),
		saltBytes,
		params.Time,
		params.Memory,
		params.Threads,
		argon2KeyLen,
	)
	return hex.EncodeToString(key)
}
