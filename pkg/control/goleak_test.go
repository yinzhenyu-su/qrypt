package control

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (debug server, driver test harness, VFS workers).
//
// lumberjack's rotation worker is ignored (see pkg/core TestMain for why
// the name carries %2e escaping). go-winio's IO completion processor is
// ignored too: the Windows named-pipe transport starts that one thread per
// process on first use and it blocks in GetQueuedCompletionStatus until the
// process exits, so the pipe tests always leave it running.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
		goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	)
}
