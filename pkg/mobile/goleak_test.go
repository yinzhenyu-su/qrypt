package mobile

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (gomobile session layer: VFS workers, task managers, stream handles).
//
// lumberjack's rotation worker is ignored (see pkg/core TestMain for why
// the name carries %2e escaping).
func TestMain(m *testing.M) {
	// Session logs go to a shared process-wide root (see testLogRoot), so
	// tests never leave an open lumberjack handle inside a t.TempDir; drop
	// whatever the previous run left behind.
	_ = os.RemoveAll(filepath.Join(os.TempDir(), testLogRoot))
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
	)
}
