//go:build windows

package core

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// spawnDetached starts exe detached from the console and releases it so the
// parent can exit independently.
func spawnDetached(exe string, args []string, logFile string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), serveChildEnv+"=1")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if logFile != "" {
		file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		cmd.Stdout = file
		cmd.Stderr = file
	}
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// terminateProcess terminates pid. Windows has no SIGTERM; the control
// endpoint is the graceful path and this is the crash-recovery fallback.
func terminateProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
