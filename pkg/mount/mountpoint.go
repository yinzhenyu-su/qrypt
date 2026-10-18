package mount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yinzhenyu/qrypt/pkg/logging"
)

// detachTimeout bounds each unmount attempt. Unmounting a mount whose daemon is
// gone can itself hang (reported with macFUSE), and `qrypt mount` must not hang
// with it. detachWaitDelay bounds the wait after the kill: a tool that spawns a
// helper (or blocks in an uninterruptible syscall) can otherwise keep the output
// pipe open long past the deadline.
const (
	detachTimeout   = 10 * time.Second
	detachWaitDelay = 2 * time.Second
)

// mountProbeTimeout bounds the daemon liveness probe in probeMountDaemon. A FUSE
// server that does not answer within it is reported as alive, because
// force-detaching a mount that is merely slow destroys a working mount, which is
// worse than surfacing the failure the user already has.
const mountProbeTimeout = 5 * time.Second

// detachCandidate is one unmount tool invocation the platform can try.
type detachCandidate struct {
	name string
	args []string
}

// ensureMountPoint makes sure the mount point exists as a directory, recovering
// from the stale mount a process that died without unmounting leaves behind.
//
// A qrypt mount that is killed hard (SIGKILL, crash, OOM killer, a signal the
// shutdown path does not handle) keeps its mount entry: the mount table still
// lists the path while every operation on it reaches a daemon that is gone.
// Linux reports that as ENOTCONN from stat(2): os.MkdirAll then takes the slow
// path, gets EEXIST from mkdir(2) because the name exists in the parent
// filesystem, fails its own Lstat re-check for the same ENOTCONN reason, and
// reports "mkdir <path>: file exists" -- which sends the user looking for a
// directory conflict that does not exist and leaves them deleting the directory
// by hand.
//
// macOS hides the dead mount better: stat(2) is answered from the dead mount's
// own root inode, so os.MkdirAll reports plain success and only the mount itself
// reveals that the directory it "found" cannot be used (the next mount then
// fails with macFUSE's "mount point ... is itself on a macFUSE volume").
// detachStaleMountPoint is what both detection paths end in.
func ensureMountPoint(mountPoint string) error {
	mkdirErr := os.MkdirAll(mountPoint, 0o755)
	if mkdirErr == nil {
		if root, dead := deadFuseMountRoot(mountPoint); dead {
			return detachStaleMountPoint(root, mountPoint)
		}
		return nil
	}

	info, statErr := os.Stat(mountPoint)
	if statErr == nil {
		if info.IsDir() {
			// Created concurrently between the two calls: nothing left to do.
			return nil
		}
		return fmt.Errorf("mount: mount point %s exists and is not a directory", mountPoint)
	}
	if !isStaleMountError(statErr) {
		// A path problem this function cannot repair (missing parent, no
		// permission, dangling symlink): keep the error the user's action caused.
		return mkdirErr
	}
	// The mount point itself is unreachable. mountRoot differs from mountPoint
	// when the path sits inside the dead mount instead of at its root.
	return detachStaleMountPoint(detachRootFor(mountPoint), mountPoint)
}

// deadFuseMountRoot reports whether path is hidden behind a FUSE mount whose
// daemon is gone, and returns the root of that mount. It is the macOS answer to
// the stat(2) errno Linux uses: the kernel serves the dead mount's root inode
// for stat(2), so only an operation that actually reaches the daemon shows that
// the connection is dead.
func deadFuseMountRoot(path string) (string, bool) {
	root, ok := fuseMountRoot(path)
	if !ok {
		return "", false
	}
	if !isDeadDaemonError(probeMountDaemon(path)) {
		// The daemon answered (or did not answer in time): the mount is usable
		// or at least not provably dead, so leave it alone.
		return "", false
	}
	return root, true
}

// detachStaleMountPoint disconnects the dead mount rooted at mountRoot and
// creates mountPoint again underneath it. mountRoot differs from mountPoint when
// the requested path sits inside the dead mount rather than at its root.
func detachStaleMountPoint(mountRoot, mountPoint string) error {
	if detachErr := detachStaleMount(mountRoot); detachErr != nil {
		return fmt.Errorf("mount: mount point %s is a stale mount left by a previous process; detach it with `%s` and retry: %w", mountRoot, detachHint(mountRoot), detachErr)
	}
	logging.L.Warnf("[FUSE] detached stale mount at %q before mounting", mountRoot)
	if err := os.MkdirAll(mountPoint, 0o755); err != nil {
		return fmt.Errorf("mount: recreate mount point %s after detaching a stale mount: %w", mountPoint, err)
	}
	return nil
}

// detachRootFor returns the path that has to be unmounted to free mountPoint: the
// root of the FUSE mount covering it when the platform reports one, and
// mountPoint itself otherwise.
func detachRootFor(mountPoint string) string {
	if root, ok := fuseMountRoot(mountPoint); ok {
		return root
	}
	return mountPoint
}

// fuseMountRoot returns the canonical root of the FUSE filesystem that contains
// path when the platform can report it (macOS does; see statfsMount).
func fuseMountRoot(path string) (string, bool) {
	root, fsType, ok := statfsMount(path)
	if !ok || root == "" || !isFuseFilesystem(fsType) {
		return "", false
	}
	return root, true
}

// isFuseFilesystem reports whether a statfs(2) filesystem type name belongs to a
// FUSE implementation. The name is platform specific ("macfuse", "osxfuse",
// "fuse.qrypt", "fuseblk", ...), so match on the shared part.
func isFuseFilesystem(fsType string) bool {
	return strings.Contains(strings.ToLower(fsType), "fuse")
}

// probeMountDaemon asks the FUSE server behind dir whether it is still there and
// returns the answer to that single question. nil means "not provably dead",
// either because the daemon answered or because it did not answer in time.
func probeMountDaemon(dir string) error {
	return boundedProbe(mountProbeTimeout, func() error {
		// A directory read is the cheapest call that reaches the daemon instead
		// of being served from the dead mount's cached root inode.
		_, err := os.ReadDir(dir)
		return err
	})
}

// boundedProbe runs probe and gives up after timeout, reporting nil -- the
// inconclusive answer -- so a caller that reads nil as "alive" stays
// conservative. The probe goroutine is left to finish on its own if it ever
// does; the alternative is blocking a mount for as long as a hung FUSE daemon
// takes to answer.
func boundedProbe(timeout time.Duration, probe func() error) error {
	done := make(chan error, 1)
	go func() {
		done <- probe()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return nil
	}
}

// detachStaleMount unmounts a mount whose daemon is gone, which is the only way
// to make the directory usable again. None of the tools need privileges beyond
// owning the mount, and the first one that succeeds wins.
func detachStaleMount(mountPoint string) error {
	return detachStaleMountWithin(mountPoint, detachTimeout)
}

// detachStaleMountWithin is detachStaleMount with an explicit per-command
// timeout, so tests can exercise the timeout path without waiting it out.
func detachStaleMountWithin(mountPoint string, timeout time.Duration) error {
	candidates := staleMountDetachers(mountPoint)
	var names, failures []string
	for _, c := range candidates {
		names = append(names, c.name)
		binary, err := exec.LookPath(c.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, binary, c.args...)
		cmd.WaitDelay = min(detachWaitDelay, timeout)
		output, err := cmd.CombinedOutput()
		// A tool that had to be killed reported success-shaped output; treat the
		// deadline as the failure it is.
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		if timedOut {
			failures = append(failures, fmt.Sprintf("%s %s: timed out after %s", c.name, strings.Join(c.args, " "), timeout))
			continue
		}
		if err == nil {
			return nil
		}
		failures = append(failures, fmt.Sprintf("%s %s: %v (%s)", c.name, strings.Join(c.args, " "), err, strings.TrimSpace(string(output))))
	}
	if len(failures) == 0 {
		return fmt.Errorf("none of %s is available in PATH", strings.Join(names, ", "))
	}
	return errors.New(strings.Join(failures, "; "))
}
