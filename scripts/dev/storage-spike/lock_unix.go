//go:build !windows

package qryptstorage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes a non-blocking exclusive advisory lock on f. The lock is held
// for as long as f (and with it the open file description) remains open, so the
// OS drops it automatically when the holder exits or is killed.
func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
