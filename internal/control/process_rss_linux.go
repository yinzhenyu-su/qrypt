//go:build linux

package control

import (
	"os"
	"strconv"
	"strings"
)

func currentProcessRSS() (uint64, string, bool) {
	body, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, "", false
	}
	fields := strings.Fields(string(body))
	if len(fields) < 2 {
		return 0, "", false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return pages * uint64(os.Getpagesize()), "/proc/self/statm", true
}
