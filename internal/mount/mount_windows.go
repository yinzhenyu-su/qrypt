//go:build windows

package mount

import "os/exec"

func unmountCommand(mountPoint string) *exec.Cmd {
	// WinFSP handles unmounting via the FUSE API (host.Unmount()).
	// The exec fallback is a best-effort attempt using the mount
	// manager utility.
	return exec.Command("net", "use", mountPoint, "/delete")
}
