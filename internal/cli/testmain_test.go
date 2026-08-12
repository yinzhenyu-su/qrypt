package cli

import (
	"os"
	"path/filepath"
	"testing"

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
	if os.Getenv("QRYPT_TEST_HOME") == "" {
		home, err := os.MkdirTemp("", "qrypt-cli-test-home-")
		if err != nil {
			panic("cli: cannot create isolated test home: " + err.Error())
		}
		os.Setenv("HOME", home)
		os.Setenv("QRYPT_HOME", filepath.Join(home, ".qrypt"))
		defer os.RemoveAll(home)
	}
	// Keep the command layer off the network: NTP queries hit DNS, whose
	// round trips are not interruptible by context cancel and would flake
	// the goleak check on slow runner DNS (see initTime).
	os.Setenv("QRYPT_TEST_NTP_DISABLED", "1")
	code := m.Run()
	util.StopNTP()
	if err := goleak.Find(
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	); err != nil {
		panic("cli: goroutine leak after tests: " + err.Error())
	}
	os.Exit(code)
}
