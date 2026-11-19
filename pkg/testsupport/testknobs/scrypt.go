package testknobs

import "github.com/yinzhenyu/qrypt/pkg/crypt"

// FastScrypt is the minimal scrypt cost cross-package tests should install
// through SetFastScryptParams. It is far below the rclone-compatible production
// cost (N=16384, r=8, p=1), so it is only safe for tests that do not assert on
// production-derived key bytes or on interoperability with an existing rclone
// remote — those vectors live in pkg/crypt and keep the production default.
var FastScrypt = crypt.ScryptParams{N: 2, R: 1, P: 1}

// SetFastScryptParams overrides pkg/crypt's default scrypt cost with FastScrypt
// and returns a function that restores the previous value.
//
// Typical use is a TestMain in a package whose tests build encrypted ciphers
// through the production config path (mount, cli) and would otherwise pay for a
// full-cost scrypt derivation on every command:
//
//	func TestMain(m *testing.M) {
//		restore := testknobs.SetFastScryptParams()
//		code := m.Run()
//		restore()
//		os.Exit(code)
//	}
//
// The override is process-wide and read atomically by
// crypt.DefaultScryptParams; install it before spawning parallel tests and call
// restore in LIFO order. It is a no-op with respect to production builds: the
// override only exists while a test calls this function.
func SetFastScryptParams() (restore func()) {
	return crypt.SetScryptParamsForTest(FastScrypt)
}
