//go:build !windows

package mount

import "os/exec"

func unmountCommand(mountPoint string) *exec.Cmd {
	return exec.Command("umount", "-f", mountPoint)
}
