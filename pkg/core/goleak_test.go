package core

import (
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
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	)
}
