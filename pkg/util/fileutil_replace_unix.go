//go:build !windows

package util

import "os"

func replaceLocalFile(source, destination string) error {
	return os.Rename(source, destination)
}
