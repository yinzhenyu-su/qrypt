//go:build !windows

package main

import "os"

func replaceLocalFile(source, destination string) error {
	return os.Rename(source, destination)
}
