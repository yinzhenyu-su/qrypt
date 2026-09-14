package core

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (Core sessions, task managers, VFS upload workers).
//
// lumberjack's rotation worker is ignored: it is a library-managed process
// goroutine started on first log write and owned by the logging package.
// Note runtime.Stack escapes '.' in package paths as %2e (Go 1.26), so the
// function name goleak sees is lumberjack%2ev2.
//
// The run is not delegated to goleak.VerifyTestMain: that helper always exits
// through os.Exit, so it can never sweep the log root after a green run. The
// leak check below is kept identical to it -- it only runs when the tests
// passed, prints the same message, and exits 1 on a leak.
func TestMain(m *testing.M) {
	logRoot := filepath.Join(os.TempDir(), testLogRoot)
	// Prune the process-wide test log dir (testRuntimeLayout writes session
	// logs there, outside any t.TempDir, so the last core's lumberjack
	// handle does not block Windows test cleanup).
	_ = os.RemoveAll(logRoot)
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
			code = 1
		}
		// Sweep this run's logs too, so a green run leaves nothing behind. The
		// last test's lumberjack sink is still open, which on its own blocks
		// the removal on Windows, so release it first: ReplaceDefault closes
		// the previous logger's file sinks and nothing else. A failure keeps
		// the logs as the post-mortem; the prune above reclaims them next run.
		logging.ReplaceDefault(logging.NewDefault())
		_ = os.RemoveAll(logRoot)
	}
	os.Exit(code)
}
