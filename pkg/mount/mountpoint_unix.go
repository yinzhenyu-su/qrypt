//go:build !windows && !darwin

package mount

import (
	"errors"
	"syscall"
)

// isStaleMountError reports whether a stat(2) error on the mount point means the
// directory is hidden behind a FUSE mount whose daemon is gone.
//
// ENOTCONN: the kernel kept the mount but the FUSE connection is closed.
// ENODEV: the fuse device vanished underneath the mount (WSL suspend,
// container restart), which surfaces the same way for stat(2).
//
// macOS needs a wider errno set and a different probe; see mountpoint_darwin.go.
func isStaleMountError(err error) bool {
	return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ENODEV)
}

// staleMountDetachers lists the unmount tools to try, in order. libfuse ships
// fusermount3/fusermount; umount is the fallback for a system without them.
func staleMountDetachers(mountPoint string) []detachCandidate {
	return []detachCandidate{
		{name: "fusermount3", args: []string{"-u", mountPoint}},
		{name: "fusermount", args: []string{"-u", mountPoint}},
		{name: "umount", args: []string{"-f", mountPoint}},
	}
}

// detachHint names the command that clears a dead mount on this platform, for
// error messages that cannot detach it themselves.
func detachHint(mountPoint string) string {
	return "fusermount3 -u " + mountPoint
}
