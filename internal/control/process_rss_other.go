//go:build !linux && !darwin && !windows

package control

func currentProcessRSS() (uint64, string, bool) {
	return 0, "", false
}
