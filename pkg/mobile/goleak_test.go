package mobile

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (gomobile session layer: VFS workers, task managers, stream handles).
//
// lumberjack's rotation worker is ignored (see pkg/core TestMain for why
// the name carries %2e escaping).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	)
}
