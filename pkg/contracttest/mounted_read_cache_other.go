//go:build !darwin && !linux

package contracttest

import "os"

func prepareColdMountedRead(_ *os.File) string { return "unsupported" }
