//go:build darwin

package mount

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/winfsp/cgofuse/fuse"
)

func TestIsStaleMountError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// macFUSE can report a mount whose daemon died as EIO; ENXIO ("device
		// not configured") is what the operations that do reach the mount layer
		// answer, and ENOTCONN/ENODEV keep parity with Linux FUSE.
		{name: "daemon gone (EIO)", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.EIO}, want: true},
		{name: "daemon gone (ENOTCONN)", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.ENOTCONN}, want: true},
		{name: "volume dropped (ENODEV)", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.ENODEV}, want: true},
		{name: "device gone (ENXIO)", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.ENXIO}, want: true},
		{name: "wrapped daemon gone", err: fmt.Errorf("stat mount point: %w", &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.EIO}), want: true},
		{name: "missing path", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.ENOENT}, want: false},
		{name: "permission denied", err: &os.PathError{Op: "stat", Path: "/Volumes/Qrypt", Err: syscall.EACCES}, want: false},
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

func TestIsDeadDaemonErrorExcludesBackendFailures(t *testing.T) {
	// The liveness probe reads a directory, and a *live* qrypt mount answers EIO
	// when the backend request fails. Detaching such a mount would make the
	// user's drive disappear, so only "the volume is not there" errnos count.
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "device gone (ENXIO)", err: syscall.ENXIO, want: true},
		{name: "daemon gone (ENOTCONN)", err: syscall.ENOTCONN, want: true},
		{name: "volume dropped (ENODEV)", err: syscall.ENODEV, want: true},
		{name: "backend failure (EIO)", err: syscall.EIO, want: false},
		{name: "missing path (ENOENT)", err: syscall.ENOENT, want: false},
		{name: "daemon answered", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeadDaemonError(tc.err); got != tc.want {
				t.Fatalf("isDeadDaemonError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestStaleMountDetachersPreferUmountThenDiskutil(t *testing.T) {
	candidates := staleMountDetachers("/Volumes/Qrypt")
	if len(candidates) != 2 {
		t.Fatalf("staleMountDetachers returned %d candidates, want 2", len(candidates))
	}
	want := []detachCandidate{
		{name: "umount", args: []string{"-f", "/Volumes/Qrypt"}},
		{name: "diskutil", args: []string{"unmount", "force", "/Volumes/Qrypt"}},
	}
	for i, c := range candidates {
		if c.name != want[i].name || strings.Join(c.args, " ") != strings.Join(want[i].args, " ") {
			t.Fatalf("candidate %d = %s %v, want %s %v", i, c.name, c.args, want[i].name, want[i].args)
		}
	}
	if hint := detachHint("/Volumes/Qrypt"); !strings.Contains(hint, "diskutil unmount force") {
		t.Fatalf("detachHint = %q, want the macFUSE recovery command", hint)
	}
}

func TestDetachStaleMountFallsBackToDiskutil(t *testing.T) {
	// umount is absent (or failing) and diskutil is the only tool left: the
	// second candidate must still be tried.
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + calls + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "diskutil"), []byte(script), 0o755); err != nil {
		t.Fatalf("write diskutil stub: %v", err)
	}
	t.Setenv("PATH", dir)

	if err := detachStaleMount("/Volumes/Qrypt"); err != nil {
		t.Fatalf("detachStaleMount: %v", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read call log: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "unmount force /Volumes/Qrypt" {
		t.Fatalf("diskutil called with %q, want %q", got, "unmount force /Volumes/Qrypt")
	}
}

func TestDetachStaleMountTimesOutAHangingUmount(t *testing.T) {
	// A hanging umount is reported with macFUSE; it must not hang qrypt mount.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "umount"), []byte("#!/bin/sh\nexec /bin/sleep 600\n"), 0o755); err != nil {
		t.Fatalf("write umount stub: %v", err)
	}
	t.Setenv("PATH", dir)

	start := time.Now()
	err := detachStaleMountWithin("/Volumes/Qrypt", 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("detachStaleMountWithin reported success although umount hung")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %q does not report the timeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("detachStaleMountWithin took %s, want it bounded by the timeout", elapsed)
	}
}

const (
	// helperMountPointEnv carries the mount point to the child process that
	// serves the filesystem the end-to-end test kills.
	helperMountPointEnv = "QRYPT_TEST_MACFUSE_MOUNT_POINT"
	helperReadyName     = "ready.txt"
	// macFuseFilesystemPath is where the macFUSE kext installs its mount helper;
	// without it no FUSE mount can be created and the test has nothing to prove.
	macFuseFilesystemPath = "/Library/Filesystems/macfuse.fs"
)

// TestEnsureMountPointDetachesDeadMacFuseMount reproduces issue #10 on macOS: a
// mount whose process is killed hard keeps its kernel mount entry, but stat(2)
// still succeeds because the kernel answers from the dead mount's root inode.
// os.MkdirAll therefore reports plain success, and the next mount fails with
//
//	mount_macfuse: mount point ... is itself on a macFUSE volume
//
// The test mounts a real macFUSE filesystem in a child process, SIGKILLs it, and
// checks that ensureMountPoint detaches the corpse -- and that a live mount is
// left alone, since force-detaching a working filesystem would be worse than the
// bug being fixed.
func TestEnsureMountPointDetachesDeadMacFuseMount(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts a real macFUSE filesystem")
	}
	if _, err := os.Stat(macFuseFilesystemPath); err != nil {
		t.Skipf("macFUSE not installed: %v", err)
	}

	mountPoint := filepath.Join(t.TempDir(), "Qrypt")
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		t.Fatalf("create mount point: %v", err)
	}

	helper, logPath := startMacFuseHelper(t, mountPoint)
	waitForMacFuseMount(t, mountPoint, logPath)

	if root, dead := deadFuseMountRoot(mountPoint); dead {
		t.Fatalf("live mount at %s reported as the dead mount rooted at %s", mountPoint, root)
	}
	if err := ensureMountPoint(mountPoint); err != nil {
		t.Fatalf("ensureMountPoint on a live mount: %v", err)
	}
	if err := macFuseReady(mountPoint); err != nil {
		t.Fatalf("ensureMountPoint broke a live mount: %v\n%s", err, readLog(logPath))
	}

	// SIGKILL skips every unmount path, so the kernel keeps the mount entry.
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = helper.Wait()
	waitForDeadMacFuseMount(t, mountPoint)

	if root, dead := deadFuseMountRoot(mountPoint); !dead {
		t.Fatalf("dead macFUSE mount at %s was not detected", mountPoint)
	} else if root == "" {
		t.Fatal("deadFuseMountRoot reported a dead mount without naming its root")
	}

	if err := ensureMountPoint(mountPoint); err != nil {
		t.Fatalf("ensureMountPoint after the helper was killed: %v\n%s", err, readLog(logPath))
	}
	if root, dead := deadFuseMountRoot(mountPoint); dead {
		t.Fatalf("mount point %s is still hidden by the dead mount rooted at %s", mountPoint, root)
	}

	info, err := os.Stat(mountPoint)
	if err != nil {
		t.Fatalf("stat mount point after recovery: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("mount point after recovery is not a directory: %v", info.Mode())
	}
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatalf("read mount point after recovery: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("mount point after recovery is not the empty directory it was created as: %v", entries)
	}
}

// TestMacFuseHelperServer is the child process of
// TestEnsureMountPointDetachesDeadMacFuseMount. It is skipped everywhere else,
// because a mount that can be killed without running a shutdown path cannot be
// created inside the test process that needs to survive it.
func TestMacFuseHelperServer(t *testing.T) {
	mountPoint := os.Getenv(helperMountPointEnv)
	if mountPoint == "" {
		t.Skip("helper process for TestEnsureMountPointDetachesDeadMacFuseMount")
	}
	host := fuse.NewFileSystemHost(&macFuseTestFS{})
	if !host.Mount(mountPoint, []string{"-o", "fsname=qrypt"}) {
		t.Fatal("mount failed")
	}
}

// macFuseTestFS is the smallest filesystem macFUSE accepts: a root directory
// holding one file, which is all the parent test needs to tell a live mount from
// a dead one.
type macFuseTestFS struct {
	fuse.FileSystemBase
}

func (macFuseTestFS) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	switch path {
	case "/":
		stat.Mode = fuse.S_IFDIR | 0o755
	case "/" + helperReadyName:
		stat.Mode = fuse.S_IFREG | 0o644
	default:
		return -fuse.ENOENT
	}
	return 0
}

func (macFuseTestFS) Readdir(path string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	fill(".", nil, 0)
	fill("..", nil, 0)
	fill(helperReadyName, nil, 0)
	return 0
}

// startMacFuseHelper runs the mount helper in a child test binary and registers
// the cleanup that keeps a failed run from leaving a dead mount behind.
func startMacFuseHelper(t *testing.T, mountPoint string) (*exec.Cmd, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "helper.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create helper log: %v", err)
	}
	t.Cleanup(func() { logFile.Close() })

	helper := exec.Command(os.Args[0], "-test.run=^TestMacFuseHelperServer$", "-test.timeout=60s")
	helper.Env = append(os.Environ(), helperMountPointEnv+"="+mountPoint)
	helper.Stdout = logFile
	helper.Stderr = logFile
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
		// A failed assertion may leave the dead mount in place; detach it so the
		// developer's machine is not left with an unusable directory.
		if _, err := os.ReadDir(mountPoint); isStaleMountError(err) {
			if detachErr := detachStaleMount(detachRootFor(mountPoint)); detachErr != nil {
				t.Logf("cleanup: detach %s: %v", mountPoint, detachErr)
			}
		}
	})
	return helper, logPath
}

func waitForMacFuseMount(t *testing.T, mountPoint, logPath string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := macFuseReady(mountPoint); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("helper never mounted %s:\n%s", mountPoint, readLog(logPath))
}

func waitForDeadMacFuseMount(t *testing.T, mountPoint string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.ReadDir(mountPoint); isStaleMountError(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("mount point %s did not become an unreachable mount after SIGKILL", mountPoint)
}

// macFuseReady reports whether the helper's filesystem is answering, which a
// dead mount never does (it fails with ENXIO, "device not configured").
func macFuseReady(mountPoint string) error {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == helperReadyName {
			return nil
		}
	}
	return fmt.Errorf("%s does not list %s", mountPoint, helperReadyName)
}

func readLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", path, err)
	}
	return string(data)
}
