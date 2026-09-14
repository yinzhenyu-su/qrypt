package mobile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yinzhenyu/qrypt/pkg/logging"
	"go.uber.org/goleak"
)

// TestMain verifies no goroutine leaks after every test in this package
// (gomobile session layer: VFS workers, task managers, stream handles).
//
// lumberjack's rotation worker is ignored (see pkg/core TestMain for why
// the name carries %2e escaping).
//
// The run is not delegated to goleak.VerifyTestMain for the same reason as
// pkg/core: it exits through os.Exit, which would skip the post-run sweep of
// the shared log root.
func TestMain(m *testing.M) {
	logRoot := filepath.Join(os.TempDir(), testLogRoot)
	// Session logs go to a shared process-wide root (see testLogRoot), so
	// tests never leave an open lumberjack handle inside a t.TempDir; drop
	// whatever the previous run left behind.
	_ = os.RemoveAll(logRoot)
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreAnyFunction("gopkg.in/natefinch/lumberjack%2ev2.(*Logger).millRun"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
			code = 1
		}
		// Sweep this run's logs too; a failure keeps them as the post-mortem
		// and the prune above reclaims them on the next run. Release the open
		// sink first, otherwise the removal cannot succeed on Windows.
		logging.ReplaceDefault(logging.NewDefault())
		_ = os.RemoveAll(logRoot)
	}
	os.Exit(code)
}
