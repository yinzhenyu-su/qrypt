//go:build fasttest

package testknobs

func init() {
	fast = true
	// Fixtures shrink 64x (6 MiB -> 96 KiB); test-only pauses collapse to zero
	// so retry loops run their attempts immediately.
	fixtureDiv = 64
	pauseFactor = 0
}
