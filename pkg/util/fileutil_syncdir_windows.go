//go:build windows

package util

// Windows replacement uses MOVEFILE_WRITE_THROUGH. Opening a directory and
// syncing its handle is not supported through os.File in the same way as Unix.
func syncParentDirectory(string) error { return nil }
