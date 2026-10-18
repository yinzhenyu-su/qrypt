//go:build windows

package mount

// isStaleMountError always reports false: WinFsp removes both the volume and the
// mount point directory when the file system process dies (verified by killing a
// mounted qrypt.exe), so there is no zombie mount to recover from.
// ensureMountPoint is not called on Windows anyway -- WinFsp owns the mount
// point directory.
func isStaleMountError(error) bool {
	return false
}

// staleMountDetachers returns nothing: isStaleMountError never reports true here,
// so detachStaleMount stays unreachable on Windows.
func staleMountDetachers(string) []detachCandidate {
	return nil
}

func detachHint(string) string {
	return ""
}
