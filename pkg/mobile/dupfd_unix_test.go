//go:build !windows

package mobile

import (
	"os"
	"syscall"
	"testing"
)

// dupFD duplicates f without attaching a Go finalizer, so the raw fd can be
// handed over and closed by the reader side without a stray runtime cleanup.
func dupFD(t *testing.T, f *os.File) int64 {
	t.Helper()
	dup, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return int64(dup)
}
