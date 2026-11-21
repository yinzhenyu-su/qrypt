package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"

	clicore "github.com/yinzhenyu/qrypt/internal/cli/core"
	cliruntime "github.com/yinzhenyu/qrypt/internal/cli/runtime"
	corepkg "github.com/yinzhenyu/qrypt/pkg/core"
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
	homeBase := ""
	if runtime.GOOS != "windows" {
		// A short home keeps derived unix control sockets under the
		// platform's 104-byte sun_path limit.
		homeBase = "/tmp"
	}
	home, err := os.MkdirTemp(homeBase, "qrypt-cli-test-home-")
	if err != nil {
		panic("cli: cannot create isolated test home: " + err.Error())
	}
	os.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE (not HOME) on Windows; redirect both
	// so "~" expansion stays inside the isolated test home.
	os.Setenv("USERPROFILE", home)
	defer os.RemoveAll(home)
	// Keep the command layer off the network: NTP queries hit DNS, whose
	// round trips are not interruptible by context cancel and would flake
	// the goleak check on slow runner DNS (see initTime).
	os.Setenv("QRYPT_TEST_NTP_DISABLED", "1")
	// Every fs command runs through the resident Core. The test binary must
	// not re-exec itself as a server, so ensureCore starts the Core in-process
	// on the same control socket a real `core serve` would use.
	clicore.EnsureCoreHook = startTestCore
	code := m.Run()
	restoreScrypt()
	util.StopNTP()
	closeTestCores()
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

var testCores []*corepkg.Core

// startTestCore runs a resident Core in-process for CLI tests: fs commands go
// through the normal Core data plane, but the server is this process instead
// of a re-executed test binary.
func startTestCore(_ context.Context, _ cliruntime.Runtime, state cliruntime.ConfigState) error {
	if state.Cfg == nil {
		return fmt.Errorf("cli test: config required for in-process Core")
	}
	runDir := corepkg.RuntimeRunDir(state.Path, state.Cfg)
	layout := corepkg.RuntimeLayout{RunDir: runDir}
	open := func() (*corepkg.Core, error) {
		return corepkg.OpenConfig(context.Background(), state.Cfg, corepkg.Options{
			ConfigPath:            state.Path,
			Runtime:               layout,
			ControlListen:         corepkg.ControlSocketPath(state.Cfg, layout),
			DisableDebugAutoStart: true,
			Owner:                 &corepkg.OwnerOptions{ConfigPath: state.Path},
		})
	}
	core, err := open()
	if err != nil {
		// Different tests use different config files but the same HOME-derived
		// work dir, so the owner lock still belongs to the previous test's
		// Core. Tests run sequentially: retire it and retry once.
		closeTestCores()
		core, err = open()
	}
	if err != nil {
		return err
	}
	testCores = append(testCores, core)
	return nil
}

func closeTestCores() {
	for _, core := range testCores {
		_ = core.Close(context.Background())
	}
	testCores = nil
}
