//go:build !linux && !darwin && !windows

package contracttest

func CurrentProcessRSS() (uint64, string, bool) {
	return 0, "", false
}
