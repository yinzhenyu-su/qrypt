//go:build !windows

package core

import (
	"os"
	"os/exec"
	"syscall"
)

// spawnDetached starts exe in its own session with no controlling terminal and
// releases it so the parent can exit independently.
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// terminateProcess sends SIGTERM to pid.
func terminateProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}
