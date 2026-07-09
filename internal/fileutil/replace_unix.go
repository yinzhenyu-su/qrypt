//go:build !windows

package fileutil

import "os"

func replaceLocalFile(source, destination string) error {
	return os.Rename(source, destination)
}
