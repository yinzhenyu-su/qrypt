// Package testknobs provides test-only scaling knobs shared by packages whose
// integration tests spend most of their wall time in production-paced waits,
// oversized fixtures, or safety limits.
//
// Without the "fasttest" build tag every helper is the identity function, so
// importing this package cannot change production or normal test behavior.
// Builds with "-tags fasttest" trade fixture fidelity for speed: fixtures shrink
// and test-only pauses collapse, while the code paths and assertions under test
// stay the same. Run:
//
//	go test -race -tags fasttest ./...
//
// Only test files (and files carrying "//go:build fasttest") may import this
// package; production code must keep its production constants.
package testknobs

import "time"

// fasttest is set by the fasttest-tagged file in this package.
var (
	fast        = false
	fixtureDiv  = 1
	pauseFactor = 1.0
)

// Fast reports whether the fasttest build tag is active.
func Fast() bool { return fast }

// FixtureBytes scales an oversized byte or JSON fixture down when the fasttest
// tag is active. It is meant for fixture sizes that only exist to cross a
// provider threshold (part sizes, chunking, page caps), not for byte-exact
// vectors.
func FixtureBytes(n int) int {
	if fixtureDiv > 1 {
		n /= fixtureDiv
	}
	if n < 1 {
		return 1
	}
	return n
}

// Pause scales a test-only retry, debounce, or poll interval. Production-paced
// constants must not be routed through it; convert only waits that exist
// solely to observe a retry/backoff loop.
func Pause(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	if pauseFactor <= 0 {
		return 0
	}
	scaled := time.Duration(float64(d) * pauseFactor)
	if scaled < time.Millisecond {
		return time.Millisecond
	}
	return scaled
}
