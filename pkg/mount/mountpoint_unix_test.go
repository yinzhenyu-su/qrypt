//go:build !windows && !darwin

package mount

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIsStaleMountError(t *testing.T) {
	stale := &os.PathError{Op: "stat", Path: "/mnt/Qrypt", Err: syscall.ENOTCONN}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "daemon gone", err: stale, want: true},
		{name: "wrapped daemon gone", err: fmt.Errorf("stat mount point: %w", stale), want: true},
		{name: "fuse device gone", err: &os.PathError{Op: "stat", Path: "/mnt/Qrypt", Err: syscall.ENODEV}, want: true},
		{name: "missing path", err: &os.PathError{Op: "stat", Path: "/mnt/Qrypt", Err: syscall.ENOENT}, want: false},
		{name: "permission denied", err: &os.PathError{Op: "stat", Path: "/mnt/Qrypt", Err: syscall.EACCES}, want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStaleMountError(tc.err); got != tc.want {
				t.Fatalf("isStaleMountError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDetachStaleMountUsesFusermount3(t *testing.T) {
	dir, calls := fakeUnmountTools(t, map[string]string{
		"fusermount3": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + "CALLS" + "\"\n",
	})

	err := detachStaleMount("/mnt/Qrypt")
	if err != nil {
		t.Fatalf("detachStaleMount: %v", err)
	}
	if got := readCalls(t, calls); got != "-u /mnt/Qrypt" {
		t.Fatalf("fusermount3 called with %q, want %q (dir %s)", got, "-u /mnt/Qrypt", dir)
	}
}

func TestDetachStaleMountFallsBackToUmount(t *testing.T) {
	_, calls := fakeUnmountTools(t, map[string]string{
		// fusermount3 is absent, so the unprivileged path cannot run.
		"umount": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + "CALLS" + "\"\n",
	})

	if err := detachStaleMount("/mnt/Qrypt"); err != nil {
		t.Fatalf("detachStaleMount: %v", err)
	}
	if got := readCalls(t, calls); got != "-f /mnt/Qrypt" {
		t.Fatalf("umount called with %q, want %q", got, "-f /mnt/Qrypt")
	}
}

func TestDetachStaleMountReportsEveryFailure(t *testing.T) {
	fakeUnmountTools(t, map[string]string{
		"fusermount3": "#!/bin/sh\necho 'Invalid argument' >&2\nexit 1\n",
	})

	err := detachStaleMount("/mnt/Qrypt")
	if err == nil {
		t.Fatal("detachStaleMount reported success although every tool failed")
	}
	for _, want := range []string{"fusermount3", "Invalid argument"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestDetachStaleMountNamesTheToolsWhenNoneIsInstalled(t *testing.T) {
	// An empty PATH: the error has to say what was looked for, because that is
	// all the user can act on.
	t.Setenv("PATH", t.TempDir())

	err := detachStaleMount("/mnt/Qrypt")
	if err == nil {
		t.Fatal("detachStaleMount reported success without any tool")
	}
	for _, want := range []string{"fusermount3", "fusermount", "umount"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestDetachHintNamesThePlatformTool(t *testing.T) {
	// detachStaleMount handles the tools itself; the hint is only for errors
	// that have to tell the user what to run.
	if got := detachHint("/mnt/Qrypt"); got != "fusermount3 -u /mnt/Qrypt" {
		t.Fatalf("detachHint = %q, want %q", got, "fusermount3 -u /mnt/Qrypt")
	}
}

func TestDetachStaleMountTimesOutAHangingTool(t *testing.T) {
	// A dead FUSE mount can make fusermount/umount hang; qrypt mount must not
	// hang with it.
	fakeUnmountTools(t, map[string]string{
		"fusermount3": "#!/bin/sh\nexec /bin/sleep 600\n",
	})

	start := time.Now()
	err := detachStaleMountWithin("/mnt/Qrypt", 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("detachStaleMountWithin reported success although the tool hung")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %q does not report the timeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("detachStaleMountWithin took %s, want it bounded by the timeout", elapsed)
	}
}

func TestDetachStaleMountDoesNotWaitForALingeringChild(t *testing.T) {
	// The killed tool's child keeps the output pipe open; WaitDelay must bound
	// the wait instead of blocking until that child exits on its own.
	fakeUnmountTools(t, map[string]string{
		"fusermount3": "#!/bin/sh\n/bin/sleep 30\n",
	})

	start := time.Now()
	err := detachStaleMountWithin("/mnt/Qrypt", 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("detachStaleMountWithin reported success although the tool hung")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("detachStaleMountWithin took %s, want WaitDelay to bound the wait", elapsed)
	}
}

// fakeUnmountTools installs executable stubs for the named unmount tools, points
// PATH at them alone, and returns the call log every stub appends to.
func fakeUnmountTools(t *testing.T, scripts map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	for name, body := range scripts {
		script := strings.ReplaceAll(body, "CALLS", calls)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
	return dir, calls
}

func readCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		t.Fatalf("read call log: %v", err)
	}
	return strings.TrimSpace(string(data))
}
