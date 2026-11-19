package testknobs

import "github.com/yinzhenyu/qrypt/pkg/crypt"

// FastArgon2 is the minimal Argon2id cost cross-package tests should install
// through SetFastArgon2Params. It is far below any production cost, so it is
// only safe for tests that do not assert on the derived password bytes — the
// production vectors live in pkg/crypt and keep DefaultArgon2Params.
var FastArgon2 = crypt.Argon2Params{Time: 1, Memory: 1024, Threads: 1}

// SetFastArgon2Params overrides pkg/crypt's default Argon2id cost with
// FastArgon2 and returns a function that restores the previous value.
//
// Typical use is a TestMain in a package whose tests call encryption APIs
// (mount, cli, log forensics) and would otherwise pay for a production-cost
// Argon2 derivation per test:
//
//	func TestMain(m *testing.M) {
//		restore := testknobs.SetFastArgon2Params()
//		code := m.Run()
//		restore()
//		os.Exit(code)
//	}
//
// The override is process-wide and read atomically by
// crypt.DefaultArgon2Params; install it before spawning parallel tests and call
// restore in LIFO order. It is a no-op with respect to production builds: the
// override only exists while a test calls this function.
func SetFastArgon2Params() (restore func()) {
	return crypt.SetArgon2ParamsForTest(FastArgon2)
}
