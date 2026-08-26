package core

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (Core sessions, task managers, VFS upload workers).
//
// lumberjack's rotation worker is ignored: it is a library-managed process
// goroutine started on first log write and owned by the logging package.
// Note runtime.Stack escapes '.' in package paths as %2e (Go 1.26), so the
// function name goleak sees is lumberjack%2ev2.
func TestMain(m *testing.M) {
	// Prune the process-wide test log dir (testRuntimeLayout writes session
	// logs there, outside any t.TempDir, so the last core's lumberjack
	// handle does not block Windows test cleanup).
	_ = os.RemoveAll(filepath.Join(os.TempDir(), testLogRoot))
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	)
}
