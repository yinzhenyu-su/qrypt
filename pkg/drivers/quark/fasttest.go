//go:build fasttest

package quark

import "time"

func init() {
	// Shorten the bounded share-task poll window. The production values (300ms
	// interval, 30s timeout) only guard against a stuck provider; tests assert
	// the polling behavior, not the wall clock.
	quarkSharePollInterval = 10 * time.Millisecond
	quarkSharePollTimeout = 2 * time.Second
}
