package read

import (
	"testing"
	"time"
)

// waitState blocks until cond is true, waking on every read-state change
// (window registration/completion, scope job start/finish, reader inflight).
// It replaces 1ms poll loops with a state-driven wake-up while keeping the
// timeout guard so a regression fails instead of hanging.
func waitState(t *testing.T, state *State, what string, cond func() bool) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-state.quiesceChanged():
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
