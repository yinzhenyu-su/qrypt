//go:build windows

package mobile

import (
	"os"
	"syscall"
	"testing"
)

// dupFD duplicates the source handle via DuplicateHandle (Windows has no
// syscall.Dup). The duplicate must outlive the test's raw-fd reads: it is
// registered for cleanup instead of relying on a finalizer.
func dupFD(t *testing.T, f *os.File) int64 {
	t.Helper()
	var dup syscall.Handle
	proc, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.DuplicateHandle(proc, syscall.Handle(f.Fd()), proc, &dup, 0, false, syscall.DUPLICATE_SAME_ACCESS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.CloseHandle(dup) })
	return int64(dup)
}
