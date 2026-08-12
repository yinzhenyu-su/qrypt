package control

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (debug server, driver test harness, VFS workers).
//
// lumberjack's rotation worker is ignored (see pkg/core TestMain for why
// the name carries %2e escaping).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	)
}
