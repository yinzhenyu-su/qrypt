package cli

import (
	"os"
	"testing"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/testsupport/testknobs"
	"github.com/yinzhenyu/qrypt/pkg/util"
	"go.uber.org/goleak"
)

// TestMain forces every CLI test away from the real home directory. Without
// this, commands that fall back to default storage paths (core's
// DefaultCacheDir/DefaultUploadDir/DefaultStateDir) write to ~/.qrypt, making
// tests depend on the host's permissions and leftover state. The redirected
// HOME also isolates every "~" expansion (config discovery, sync persist).
//
// goleak then verifies the command runs left no goroutines behind.
//
// One-shot CLI commands start the NTP sync loop with a context that lives
// for the command only; without a parent cancel the last command's loop
// would outlive the tests. StopNTP drains it before goleak checks.
func TestMain(m *testing.M) {
	// Every encrypted mount and crypt-encode/decode command builds a fresh
	// rclone cipher, which derives its keys with scrypt at rclone's on-disk
	// contract cost (N=16384, r=8, p=1, ~271ms per build under -race). No CLI
	// test asserts production-derived key bytes or talks to an existing rclone
	// remote: internal/cli has no testdata or golden fixtures, and the raw/cat
	// comparisons are in-process round trips. Installing the tiny test cost
	// keeps the production derivation path exercised without the per-command
	// KDF. It is process-wide, so install it before any test starts.
	restoreScrypt := testknobs.SetFastScryptParams()
	home, err := os.MkdirTemp("", "qrypt-cli-test-home-")
	if err != nil {
		panic("cli: cannot create isolated test home: " + err.Error())
	}
	os.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE (not HOME) on Windows; redirect both
	// so "~" expansion stays inside the isolated test home.
	os.Setenv("USERPROFILE", home)
	os.Unsetenv("QRYPT_HOME")
	defer os.RemoveAll(home)
	// Idle waits poll until queued work drains; the production cadence is 50ms
	// and most commands end with one, so tests would spend their runtime
	// sleeping on the timer. Polling faster keeps the same loop and the same
	// semantics, only the observation cadence changes.
	fileSystemIdlePollInterval = 5 * time.Millisecond
	// Keep the command layer off the network: NTP queries hit DNS, whose
	// round trips are not interruptible by context cancel and would flake
	// the goleak check on slow runner DNS (see initTime).
	os.Setenv("QRYPT_TEST_NTP_DISABLED", "1")
	// Exercise the legacy in-process VFS path by default; tests that cover the
	// resident-Core data plane opt back in with QRYPT_CORE_FS_REQUIRED=1.
	os.Setenv("QRYPT_CORE_FS_REQUIRED", "0")
	code := m.Run()
	restoreScrypt()
	util.StopNTP()
	if err := goleak.Find(
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
		// go-winio creates one process-global IO completion port and the
		// goroutine that services it on the first named-pipe use. It lives
		// until the process exits, so a probe server opened by a test leaves
		// a library lifetime behind, not a test leak.
		goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	); err != nil {
		panic("cli: goroutine leak after tests: " + err.Error())
	}
	// os.Exit does not run the deferred removal above, which only the panic
	// path reaches, so drop the isolated home explicitly here. Whatever a
	// still-open handle keeps behind (Windows) is swept by test-layers.sh on
	// the next layer run.
	os.RemoveAll(home)
	os.Exit(code)
}
